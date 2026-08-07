package run

import (
	"context"
	"log/slog"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
)

// Halt stops an in-flight run (FEAT-024): the run's background execution context is cancelled —
// which aborts the current step's model/engine work (IMPLEMENTATION.md §4: cancellation is
// load-bearing) — and the run is marked halted so the Run Viewer pivots to the halted state.
//
// The step rows themselves are settled to "halted" by the run's engine goroutine as its cancelled
// work unwinds (see stepFailed → haltRun), not by this handler, so the persisted state is a clean,
// resumable halt rather than a failure the user never caused, and no two writers race a step row.
// A run still awaiting the engine wake has no in-flight step yet; it simply reads halted.
func (e *Executor) Halt(userID, runID string) {
	go e.halt(runID)
}

func (e *Executor) halt(runID string) {
	ctx := context.Background()
	run, err := e.runs.GetByID(ctx, runID)
	if err != nil {
		e.logger.Error("halting run: loading", slog.String("run_id", runID), slog.String("error", err.Error()))
		return
	}
	log := e.logger.With(slog.String("run_id", run.ID), slog.String("workspace_id", run.WorkspaceID))

	// A terminal run has nothing to stop: halting a completed/failed/halted run is a no-op, not an
	// error (a user clicking Halt on a just-finished run should see nothing break).
	if run.Status.IsTerminal() {
		log.Debug("halt: run already terminal", slog.String("status", string(run.Status)))
		return
	}

	// Cancel first so the engine goroutine's next checkpoint unwinds; then reflect halted so the UI
	// pivots immediately. The engine goroutine settles the step rows (haltRun) and re-asserts halted,
	// so the final persisted state is deterministic.
	e.cancelRun(run.ID)
	if err := e.setRunStatus(ctx, log, run.ID, domain.RunHalted); err != nil {
		log.Error("halting run: marking run halted", slog.String("error", err.Error()))
		return
	}
	log.Info("run halted")
}

// stepFailed is the executor's single step-failure choke point. If the run's context was cancelled
// (a user halt raced the step, FEAT-024), the run is settled to halted instead of failed — a halt is
// not a failure, and the UI must not read one as the other. Otherwise the ordinary fail path runs.
func (e *Executor) stepFailed(ctx context.Context, log *slog.Logger, run domain.Run) {
	if ctx.Err() == context.Canceled {
		e.haltRun(ctx, log, run)
		return
	}
	e.failRun(ctx, log, run.ID)
}

// haltRun settles a run's unfinished step rows to halted and marks the run halted. It is called by
// the run's engine goroutine after a cancelled step unwinds (stepFailed) — never by the Halt
// handler directly — so a step row is written by exactly one writer. Completed rows are never
// touched; the settlement is idempotent across the (few) steps a linear run may have persisted.
func (e *Executor) haltRun(ctx context.Context, log *slog.Logger, run domain.Run) {
	if rows, err := e.steps.ListByRun(ctx, run.ID); err == nil {
		for _, st := range rows {
			if st.Status == domain.StepComplete {
				continue
			}
			if err := e.steps.Halt(ctx, st.ID); err != nil {
				log.Warn("halting step", slog.String("step", string(st.StepName)), slog.String("error", err.Error()))
				continue
			}
			// Mirror orchestrator's step.completed/failed events (FEAT-023): the viewer reconciles
			// by step id, so publishing the settled step lets it flip the row to halted live.
			e.publish(run.ID, events.Event{
				Type:       events.TypeStepHalted,
				RunID:      run.ID,
				StepName:   string(st.StepName),
				StepID:     st.ID,
				StepStatus: string(domain.StepHalted),
			})
		}
	} else {
		log.Warn("halting run: listing steps", slog.String("error", err.Error()))
	}
	if err := e.setRunStatus(ctx, log, run.ID, domain.RunHalted); err != nil {
		log.Error("halting run: marking run halted", slog.String("error", err.Error()))
	}
}
