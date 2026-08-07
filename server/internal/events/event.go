// Package events types the transient live-run event stream (FEAT-023).
//
// This stream is deliberately NOT persisted: it is the in-progress state of a run as it executes,
// useful only while a viewer is attached. The durable source of truth is always the runs/steps rows
// the orchestrator writes; the event stream is a low-latency push so the Run Viewer shows each DAG
// transition as it happens instead of polling. On any disconnect the viewer re-reads the persisted
// run + steps (the snapshot an SSE handler replays) and reconnects, so no event is ever load-bearing.
package events

// Type identifies an event in the live run stream.
type Type string

// Event types. All events are emitted without an SSE `event:` field, so the browser's generic
// `onmessage` handler receives them as JSON text (see web/src/lib/api-client.ts subscribeToStream).
const (
	// TypeRunStatus is emitted on every run-status transition (waking, running, complete, failed).
	// Status carries the run's RunStatus.
	TypeRunStatus Type = "run.status"
	// TypeStepStarted is emitted when a step's work actually begins (a step row is claimed).
	TypeStepStarted Type = "step.started"
	// TypeStepCompleted is emitted when a step's output is persisted.
	// Output carries the step's JSON-encoded output.
	TypeStepCompleted Type = "step.completed"
	// TypeStepFailed is emitted when a step's work fails and the step row is marked failed.
	TypeStepFailed Type = "step.failed"
	// TypeCacheHit is emitted when a run was answered entirely from determinism cache (FEAT-027):
	// plan/generate_sql/execute/chart_spec/narrate were replayed from a prior identical run, and no
	// model or engine work happened. CachedRunID names that prior run.
	TypeCacheHit Type = "cache.hit"
	// TypeStreamEnd marks the end of a stream: emitted after the run's terminal event so the viewer
	// can stop listening. Not persisted; purely a streaming delimiter.
	TypeStreamEnd Type = "stream.end"
)

// Event is one record in the transient live-run stream.
//
// Fields are sparse by design: each Type populates only the fields it means (a run.status carries
// Status; a step.completed carries StepID/StepName/StepStatus/Output). The viewer ignores events it
// has already reconciled against the persisted steps by step id, so an out-of-order or duplicated
// delivery can never double-count a transition.
type Event struct {
	Type        Type   `json:"type"`
	RunID       string `json:"run_id"`
	Status      string `json:"status,omitempty"`
	StepName    string `json:"step_name,omitempty"`
	StepID      string `json:"step_id,omitempty"`
	StepStatus  string `json:"step_status,omitempty"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
	CachedRunID string `json:"cached_run_id,omitempty"`
}
