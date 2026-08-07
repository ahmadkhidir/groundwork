package run

import (
	"context"
	"log/slog"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Resume restarts background execution of a previously-interrupted run from its first unfinished
// step (FEAT-022, risk_log R-006 engine-loss recovery).
//
// It is safe to call for any run: the step machine replays every already-COMPLETED step (never re-
// running model or engine work) and reclaims the first unfinished one, so the caller cannot double-
// execute a step. A FAILED or HALTED run is resumable: Resume re-claims its unfinished step into a
// fresh attempt (an idempotent retry that RE-USES the step's UNIQUE idempotency key via
// orchestrator's Reset rather than colliding on it) — FEAT-022's "idempotent retry" in durable form.
// A COMPLETED run is a no-op, and a queued/waking/running run (already being driven by the executor)
// is skipped rather than double-driven.
func (e *Executor) Resume(userID, runID string) {
	ctx, cancel := e.runContext(runID)
	go func() {
		defer cancel()
		defer e.releaseRunContext(runID)
		e.resume(ctx, userID, runID)
	}()
}

func (e *Executor) resume(ctx context.Context, userID, runID string) {
	run, err := e.runs.GetByID(ctx, runID)
	if err != nil {
		e.logger.Error("resuming run: loading", slog.String("run_id", runID), slog.String("error", err.Error()))
		return
	}
	log := e.logger.With(slog.String("run_id", run.ID), slog.String("workspace_id", run.WorkspaceID))

	// Only a terminal-bad or interrupted run is resumable: failed/halted reclaim their unfinished
	// step (idempotent retry). A complete run has nothing honest to resume, and a queued/waking/
	// running run is already being driven by the executor — re-entering it would race the live
	// execution, so it is skipped rather than double-driven.
	switch run.Status {
	case domain.RunFailed, domain.RunHalted:
		// Resumable.
	case domain.RunComplete:
		log.Debug("resume: run already complete", slog.String("status", string(run.Status)))
		return
	default:
		log.Debug("resume: run already in progress", slog.String("status", string(run.Status)))
		return
	}

	// Decide whether the pipeline owns a plan step. A COMPLETED generate_sql proves what this run is:
	// if it has no plan row it was a seeded (FEAT-018) run and the resume must not fabricate one.
	includePlan := true
	if ledger, err := e.engine.Load(ctx, run.ID); err == nil {
		if _, hasGenerate := ledger[domain.StepGenerateSQL]; hasGenerate {
			if _, hasPlan := ledger[domain.StepPlan]; !hasPlan {
				includePlan = false
			}
		}
	} else {
		log.Warn("resume: loading ledger for plan inference", slog.String("error", err.Error()))
	}

	e.executePipeline(ctx, userID, run, nil, includePlan)
}
