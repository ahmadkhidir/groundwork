package run

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// AmendAndResume restarts a run after the user amended a step's input (FEAT-024): the amended step
// and every step after it in pipeline order are invalidated for re-execution (downstream
// invalidation — Decision 2, Option A), steps before it are preserved untouched, and execution
// resumes from the amended step forward. Resuming never re-runs the unaffected completed steps
// before the amendment (FEAT-024 AC3), and it re-uses existing step rows so the schema's UNIQUE
// idempotency_key is never violated.
//
// This wave's linear executor supports amending generate_sql — the Run Viewer's "edit generated
// SQL" affordance. newInput is the edited SQL, which seeds the generate_sql step exactly like
// FEAT-018's edited-query path (no model re-ask); everything downstream re-executes. Amending any
// other step is logged as unsupported rather than half-implemented.
func (e *Executor) AmendAndResume(userID, runID string, stepName domain.StepName, newInput string) {
	ctx, cancel := e.runContext(runID)
	go func() {
		defer cancel()
		defer e.releaseRunContext(runID)
		e.amendAndResume(ctx, userID, runID, stepName, newInput)
	}()
}

func (e *Executor) amendAndResume(ctx context.Context, userID, runID string, stepName domain.StepName, newInput string) {
	run, err := e.runs.GetByID(ctx, runID)
	if err != nil {
		e.logger.Error("amending run: loading", slog.String("run_id", runID), slog.String("error", err.Error()))
		return
	}
	log := e.logger.With(slog.String("run_id", run.ID), slog.String("workspace_id", run.WorkspaceID))

	// Only the Run Viewer's SQL-edit amend is in this wave's scope; anything else is an honest
	// no-op rather than a half-built re-run of an arbitrary step.
	if stepName != domain.StepGenerateSQL {
		log.Warn("amend: unsupported step in this wave", slog.String("step_name", string(stepName)))
		return
	}
	if strings.TrimSpace(newInput) == "" {
		log.Warn("amend: empty edited SQL, refusing", slog.String("step_name", string(stepName)))
		return
	}

	// Downstream invalidation (Decision 2): flip generate_sql and every step after it (static_check,
	// execute, chart_spec, narrate) into a fresh running attempt so the engine re-runs them. The
	// store re-claims EXISTING rows, so the idempotency_key UNIQUE constraint is respected.
	// Steps before generate_sql (plan) are untouched — orchestrator's "completed steps are never
	// reset" contract holds for them and they replay unchanged on resume.
	if err := e.steps.InvalidateFromStep(ctx, run.ID, stepName); err != nil {
		log.Error("amending run: invalidating downstream", slog.String("error", err.Error()))
		return
	}

	// Resume from the amended step: seed generate_sql with the edited SQL (no model call) and run
	// the ordinary seeded tail (static_check → execute → chart_spec → narrate → report). Invalidated
	// downstream steps are re-run via the step machine's reset path; unaffected prior steps replay.
	e.executePipeline(ctx, userID, run, &newInput, false)
}
