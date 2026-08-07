package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// memStepStore is an in-memory orchestrator.StepStore for unit tests.
type memStepStore struct {
	mu          sync.Mutex
	byRun       map[string][]domain.Step
	resetIDs    []string
	createCount int
}

func newMemStepStore() *memStepStore {
	return &memStepStore{byRun: make(map[string][]domain.Step)}
}

func (m *memStepStore) ListByRun(_ context.Context, runID string) ([]domain.Step, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Step, len(m.byRun[runID]))
	copy(out, m.byRun[runID])
	return out, nil
}

func (m *memStepStore) Create(_ context.Context, _ string, step domain.Step) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCount++
	m.byRun[step.RunID] = append(m.byRun[step.RunID], step)
	return nil
}

func (m *memStepStore) Reset(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, runID := range m.byRun {
		for i := range runID {
			if runID[i].ID == id {
				runID[i].Status = domain.StepRunning
				m.resetIDs = append(m.resetIDs, id)
				return nil
			}
		}
	}
	return errors.New("step not found")
}

func (m *memStepStore) Complete(_ context.Context, id, output string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.byRun {
		for i := range run {
			if run[i].ID == id {
				run[i].Status = domain.StepComplete
				run[i].Output = output
				return nil
			}
		}
	}
	return errors.New("step not found")
}

func (m *memStepStore) Fail(_ context.Context, id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.byRun {
		for i := range run {
			if run[i].ID == id {
				run[i].Status = domain.StepFailed
				run[i].Output = reason
				return nil
			}
		}
	}
	return errors.New("step not found")
}

func (m *memStepStore) snapshot(runID string) []domain.Step {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Step, len(m.byRun[runID]))
	copy(out, m.byRun[runID])
	return out
}

// seed inserts a pre-existing row without counting it as a Create, mirroring a run that already has
// persisted steps (e.g. a crashed attempt the test then resumes).
func (m *memStepStore) seed(step domain.Step) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byRun[step.RunID] = append(m.byRun[step.RunID], step)
}

func (m *memStepStore) resets() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.resetIDs))
	copy(out, m.resetIDs)
	return out
}

// recordingEmitter captures every Publish call for assertion.
type recordingEmitter struct {
	mu  sync.Mutex
	evs []events.Event
}

func (e *recordingEmitter) Publish(_ string, ev events.Event) {
	e.mu.Lock()
	e.evs = append(e.evs, ev)
	e.mu.Unlock()
}

func (e *recordingEmitter) all() []events.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]events.Event, len(e.evs))
	copy(out, e.evs)
	return out
}

func newEngine(t *testing.T) (*Engine, *memStepStore, *recordingEmitter) {
	t.Helper()
	store := newMemStepStore()
	em := &recordingEmitter{}
	return NewEngine(store, em, discardLogger()), store, em
}

func callFor(runID string) Call {
	return Call{
		RunID:          runID,
		WorkspaceID:    "ws-1",
		StepName:       domain.StepGenerateSQL,
		IdempotencyKey: runID + ":generate_sql",
	}
}

func TestEnsureStepClaimsRunsWorkAndCompletes(t *testing.T) {
	e, store, em := newEngine(t)
	ledger := map[domain.StepName]domain.Step{}
	workCount := 0

	output, reused, err := e.EnsureStep(t.Context(), ledger, callFor("run-1"), func() (string, error) {
		workCount++
		return "out-1", nil
	})
	if err != nil {
		t.Fatalf("EnsureStep: %v", err)
	}
	if output != "out-1" || reused {
		t.Fatalf("output=%q reused=%v, want out-1 false", output, reused)
	}
	if workCount != 1 {
		t.Fatalf("work invoked %d times, want 1", workCount)
	}
	if store.createCount != 1 {
		t.Fatalf("Create invoked %d times, want 1", store.createCount)
	}
	if got := ledger[domain.StepGenerateSQL].Output; got != "out-1" {
		t.Fatalf("ledger output = %q, want out-1", got)
	}

	// step.started then step.completed, in that order, with the output.
	evs := em.all()
	if len(evs) != 2 {
		t.Fatalf("events = %d (%+v), want 2", len(evs), evs)
	}
	if evs[0].Type != events.TypeStepStarted || evs[1].Type != events.TypeStepCompleted {
		t.Fatalf("event sequence = [%s, %s], want [started, completed]", evs[0].Type, evs[1].Type)
	}
	if evs[1].Output != "out-1" {
		t.Fatalf("completed event output = %q, want out-1", evs[1].Output)
	}
}

func TestEnsureStepReplaysCompletedWithoutWork(t *testing.T) {
	e, _, em := newEngine(t)
	now := time.Now().UTC()
	prior := domain.Step{
		ID: "step-1", RunID: "run-1", StepName: domain.StepGenerateSQL,
		Status: domain.StepComplete, Output: "cached-out", StartedAt: &now,
	}
	ledger := map[domain.StepName]domain.Step{domain.StepGenerateSQL: prior}
	workCount := 0

	output, reused, err := e.EnsureStep(t.Context(), ledger, callFor("run-1"), func() (string, error) {
		workCount++
		return "fresh", nil
	})
	if err != nil {
		t.Fatalf("EnsureStep: %v", err)
	}
	if output != "cached-out" || !reused {
		t.Fatalf("output=%q reused=%v, want cached-out true", output, reused)
	}
	if workCount != 0 {
		t.Fatalf("work invoked %d times, want 0 (replay)", workCount)
	}
	if got := em.all(); len(got) != 0 {
		t.Fatalf("no events expected on replay, got %+v", got)
	}
}

func TestEnsureStepResetsNonCompletedRowForRetry(t *testing.T) {
	e, store, _ := newEngine(t)
	now := time.Now().UTC()
	prior := domain.Step{
		ID: "step-1", RunID: "run-1", StepName: domain.StepGenerateSQL,
		Status: domain.StepFailed, Output: "old failure", StartedAt: &now,
	}
	store.seed(prior)
	ledger := map[domain.StepName]domain.Step{domain.StepGenerateSQL: prior}
	workCount := 0

	output, reused, err := e.EnsureStep(t.Context(), ledger, callFor("run-1"), func() (string, error) {
		workCount++
		return "retried", nil
	})
	if err != nil {
		t.Fatalf("EnsureStep: %v", err)
	}
	if output != "retried" || reused {
		t.Fatalf("output=%q reused=%v, want retried false", output, reused)
	}
	if workCount != 1 {
		t.Fatalf("work invoked %d times, want 1 (retry)", workCount)
	}
	// The retry re-uses the SAME row (Reset), never inserting a duplicate idempotency key.
	if store.createCount != 0 {
		t.Fatalf("Create invoked %d times, want 0 (reset path)", store.createCount)
	}
	if resets := store.resets(); len(resets) != 1 || resets[0] != "step-1" {
		t.Fatalf("Reset(step-1) not recorded: %+v", resets)
	}
}

func TestEnsureStepReclaimsStaleRunningRow(t *testing.T) {
	e, store, _ := newEngine(t)
	now := time.Now().UTC()
	prior := domain.Step{
		ID: "step-1", RunID: "run-1", StepName: domain.StepGenerateSQL,
		Status: domain.StepRunning, StartedAt: &now,
	}
	store.seed(prior)
	ledger := map[domain.StepName]domain.Step{domain.StepGenerateSQL: prior}

	if _, _, err := e.EnsureStep(t.Context(), ledger, callFor("run-1"), func() (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("EnsureStep: %v", err)
	}
	if store.createCount != 0 {
		t.Fatalf("Create invoked %d times, want 0 (reclaim)", store.createCount)
	}
	if resets := store.resets(); len(resets) != 1 {
		t.Fatalf("expected one reset, got %+v", resets)
	}
}

func TestLoadReturnsMostRecentRowPerStepName(t *testing.T) {
	e, _, _ := newEngine(t)
	_ = e.steps.Create(t.Context(), "ws-1", domain.Step{ID: "a", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepFailed, IdempotencyKey: "run-1:plan"})
	_ = e.steps.Create(t.Context(), "ws-1", domain.Step{ID: "b", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete, Output: "second", IdempotencyKey: "run-1:plan"})

	ledger, err := e.Load(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := ledger[domain.StepPlan].ID; got != "b" {
		t.Fatalf("ledger plan id = %s, want b (last row wins)", got)
	}
	if got := ledger[domain.StepPlan].Status; got != domain.StepComplete {
		t.Fatalf("ledger plan status = %s, want complete", got)
	}
}
