package ingest

import (
	"sync"
	"time"
)

// Step is a fine-grained, in-flight ingest step, finer than domain.IngestStatus's four persisted
// checkpoints (pending/provisioning/profiling/failed). screens/06-ingest-progress.md's step
// indicator distinguishes "Provisioning engine" from "Loading data", but schema.md's ingest_status
// enum has no room for that split — both persist as "provisioning" (see migration 00003's comment on
// why a free-text reason got its own column instead of widening the enum the same way). Step is the
// live, in-memory answer to "which of the two am I in right now"; it does not need to survive a
// server restart, only a browser tab close, which an in-process map already does.
type Step string

// Steps this package (FEAT-004) drives directly.
const (
	StepProvisioning Step = "provisioning"
	StepLoading      Step = "loading"
)

// Steps that exist on the screen but are NOT driven by this package — owned by later waves. A
// dataset sitting at StepProfiling or StepRelationships here just reflects "FEAT-004 finished and
// handed off"; it is never fabricated to look further along than it is.
const (
	StepProfiling     Step = "profiling"
	StepRelationships Step = "relationships"
	StepFailed        Step = "failed"
)

// State is one dataset's current live ingest state, as shown on the Ingest Progress screen.
type State struct {
	Step Step
	// Error is the specific, actionable failure reason (FEAT-004's acceptance criterion), populated
	// only when Step is StepFailed.
	Error     string
	UpdatedAt time.Time
}

// Tracker holds the live, in-memory ingest step for every dataset currently being processed by this
// server process. It is deliberately NOT the durable record of ingest state — domain.IngestStatus in
// Postgres is — so a server restart mid-ingest loses only the fine-grained "which sub-step" detail,
// never the coarse state a returning user needs (screens/06's edge case: "user closes the tab
// mid-ingest -> ingest continues server-side... rather than losing progress").
type Tracker struct {
	mu     sync.RWMutex
	states map[string]State
}

// NewTracker constructs an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{states: make(map[string]State)}
}

// Set records datasetID's current step.
func (t *Tracker) Set(datasetID string, step Step, errMsg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states[datasetID] = State{Step: step, Error: errMsg, UpdatedAt: time.Now()}
}

// Claim atomically records datasetID as StepProvisioning and reports true, UNLESS this process
// already has a live entry for it, in which case it changes nothing and reports false.
//
// This is Pipeline.Start's actual idempotency guard against a same-process race: Start's own
// "ds.IngestStatus != pending" check reads Postgres, which is eventually consistent with a
// just-started run — a second POST /ingest arriving before the first goroutine's SetIngestStatus
// write lands would otherwise see "pending" too and spawn a second, fully concurrent run() for the
// same dataset (duplicate profiling, duplicate column rows, a unique-constraint failure surfaced as
// an honest-looking but false ingest failure). Claim closes that window in-process, instantly,
// without waiting on a database round trip.
func (t *Tracker) Claim(datasetID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.states[datasetID]; exists {
		return false
	}
	t.states[datasetID] = State{Step: StepProvisioning, UpdatedAt: time.Now()}
	return true
}

// Get returns datasetID's live state, if this process has one (it may not, e.g. after a restart or
// for a dataset this process never started processing — callers fall back to the persisted
// ingest_status in that case).
func (t *Tracker) Get(datasetID string) (State, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.states[datasetID]
	return s, ok
}

// Clear removes datasetID's live state once ingest reaches a terminal or handed-off state, so the
// map does not grow unboundedly across a long-running process.
func (t *Tracker) Clear(datasetID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, datasetID)
}
