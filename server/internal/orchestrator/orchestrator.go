// Package orchestrator is the durable run DAG step machine (FEAT-022).
//
// This is the home of PER-PERSISTENCE and RETRY, the two guarantees a resumable run rests on
// (risk_log R-005 / R-006):
//
//   - Every step's execution is idempotent on (run, step): a step is keyed by runID:stepName, and
//     once a run has a COMPLETED step for a name, that step's work is never re-run — the prior output
//     is replayed. That is what lets a crashed or resumed run pick up from its first incomplete
//     step instead of starting over (or worse, double-executing model/engine calls).
//
//   - A step's row is created with status "running" the moment its work begins, and transitioned to
//     "complete"/"failed" in the same follow-ups that write its output. A process torn down
//     mid-step therefore leaves a clearly re-runnable state as the next checkpoint.
//
// This package is deliberately schedulers-narrow: it owns only the claim/persist/replay step loop
// and the live event emission that makes it observable (FEAT-023). The actual model/engine WORK for a
// step is provided by the caller through a work func — package run wires the top-level DAG nodes
// (plan → generate_sql → execute → chart_spec → narrate) and their model/engine calls in; this
// engine never imports model or engine, keeping the step machine unit-testable with a plain func.
package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
)

// newStepID returns a fresh id for a claimed step row.
func newStepID() string {
	return uuid.NewString()
}

// RunStore is the seam to run rows. Satisfied by postgres.RunRepository.
type RunStore interface {
	GetByID(ctx context.Context, id string) (domain.Run, error)
	UpdateStatus(ctx context.Context, id string, status domain.RunStatus) error
}

// StepStore is the seam to step rows. Satisfied by postgres.StepRepository.
//
// Load feeds the resume ledger; Create inserts a freshly-claimed row; Reset re-claims an existing
// non-completed row for an idempotent retry (it must RE-USE the row rather than insert, because the
// schema's UNIQUE idempotency_key would reject a duplicate); Complete and Fail settle a row.
type StepStore interface {
	ListByRun(ctx context.Context, runID string) ([]domain.Step, error)
	Create(ctx context.Context, workspaceID string, step domain.Step) error
	// Reset re-claims step id (status running, attempt bumped, completion cleared) for a retry that
	// resumes an earlier attempt without violating the idempotency key's UNIQUE constraint. Returns
	// ErrStepNotClaimable if the row is already complete (a completed step is never reset).
	Reset(ctx context.Context, id string) error
	Complete(ctx context.Context, id, output string) error
	Fail(ctx context.Context, id, reason string) error
}

// Emitter pushes live events to the run's stream feed (FEAT-023). Satisfied by *events.Hub.
type Emitter interface {
	Publish(runID string, ev events.Event)
}

// Call is the identity+input a step is claimed with: a run's step is a pure-ish function of this
// (run, stepName, input) triple. The IdempotencyKey is runID:stepName, deterministic per (run, step).
type Call struct {
	RunID          string
	WorkspaceID    string
	StepName       domain.StepName
	Input          string
	IdempotencyKey string
}

// Engine is the step machine. It holds a StepStore and an Emitter, neither of which it mutates
// beyond the calls they expose. Construct once per executor and reuse for every run — it is safe for
// concurrent use (store + emitter are).
type Engine struct {
	steps   StepStore
	emitter Emitter
	logger  *slog.Logger
}

// NewEngine builds a step machine over the given step store, emitting events to em and logging to
// logger.
func NewEngine(steps StepStore, em Emitter, log *slog.Logger) *Engine {
	return &Engine{steps: steps, emitter: em, logger: log}
}

// Load returns the resume ledger: for each step name, the MOST RECENT row of runID. The last row per
// name is chosen because that is (a) the COMPLETED terminal row when the step finished (replay it),
// or (b) the failed/halted/stale-running row a retry must reclaim (Reset) rather than collide with.
func (e *Engine) Load(ctx context.Context, runID string) (map[domain.StepName]domain.Step, error) {
	rows, err := e.steps.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.StepName]domain.Step)
	for _, st := range rows {
		out[st.StepName] = st
	}
	return out, nil
}

// EnsureStep runs work for a step with idempotency and resumability (FEAT-022):
//
//   - A COMPLETED row already exists for (run, step) → replay its output; work is NOT re-run.
//   - A non-completed row exists (a failed/abandoned attempt) → RESET it (a fresh attempt re-using
//     the same UNIQUE idempotency key) and run work.
//   - No row exists → CREATE a claimed running row, then run work.
//
// It emits step.started on each actual attempt and step.completed / step.failed on settle, so the
// live stream (FEAT-023) reflects the transition. The returned output is the persisted (or replayed)
// output; reused=true means a completed row was replayed with no work.
func (e *Engine) EnsureStep(ctx context.Context, ledger map[domain.StepName]domain.Step, call Call, work func() (string, error)) (output string, reused bool, err error) {
	if prior, ok := ledger[call.StepName]; ok && prior.Status == domain.StepComplete {
		return prior.Output, true, nil
	}

	stepID := ""
	if prior, ok := ledger[call.StepName]; ok {
		// A non-completed row already claims this idempotency key: reset it into a fresh attempt
		// rather than colliding on the UNIQUE constraint (R-005's idempotent retry).
		if err := e.steps.Reset(ctx, prior.ID); err != nil {
			return "", false, err
		}
		stepID = prior.ID
	} else {
		now := time.Now().UTC()
		step := domain.Step{
			ID:             newStepID(),
			RunID:          call.RunID,
			StepName:       call.StepName,
			Status:         domain.StepRunning,
			Input:          call.Input,
			IdempotencyKey: call.IdempotencyKey,
			StartedAt:      &now,
		}
		if err := e.steps.Create(ctx, call.WorkspaceID, step); err != nil {
			return "", false, err
		}
		stepID = step.ID
	}

	e.emit(call.RunID, events.Event{
		Type: events.TypeStepStarted, RunID: call.RunID,
		StepName: string(call.StepName), StepID: stepID, StepStatus: string(domain.StepRunning),
	})

	out, workErr := work()
	if workErr != nil {
		if failErr := e.steps.Fail(ctx, stepID, workErr.Error()); failErr != nil {
			e.logger.Error("persisting step failure", slog.String("run_id", call.RunID), slog.String("step", string(call.StepName)), slog.String("error", failErr.Error()))
		}
		e.emit(call.RunID, events.Event{
			Type: events.TypeStepFailed, RunID: call.RunID,
			StepName: string(call.StepName), StepID: stepID, StepStatus: string(domain.StepFailed),
			Output: workErr.Error(),
		})
		return "", false, workErr
	}

	if err := e.steps.Complete(ctx, stepID, out); err != nil {
		return "", false, err
	}
	ledger[call.StepName] = domain.Step{StepName: call.StepName, Output: out, Status: domain.StepComplete}
	e.emit(call.RunID, events.Event{
		Type: events.TypeStepCompleted, RunID: call.RunID,
		StepName: string(call.StepName), StepID: stepID, StepStatus: string(domain.StepComplete),
		Output: out,
	})
	return out, false, nil
}

// emit publishes ev to the emitter, if one is wired.
func (e *Engine) emit(runID string, ev events.Event) {
	if e.emitter != nil {
		e.emitter.Publish(runID, ev)
	}
}
