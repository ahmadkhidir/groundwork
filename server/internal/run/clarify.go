package run

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// This file implements FEAT-033 (Honest Uncertainty), Verification Layer 7: when the plan step
// judges a question ambiguous ("top customers" by revenue or count? over what period?), the run
// pauses with a SPECIFIC clarifying question instead of failing or guessing (screen 12).
//
// Mechanics (see clarifyAndResume for the resume half):
//
//  1. The plan step's output carries needs_clarification, the specific question, discrete options
//     (when the ambiguity has an enumerable resolution), and a stable ambiguity_key naming the
//     ambiguity so a functionally identical follow-up can be recognized.
//  2. executePipeline pauses the run (settled to the existing `halted` status — the DB status enum
//     has no dedicated "awaiting clarification" value, so the Run Viewer distinguishes the paused
//     state by the plan step's needs_clarification rather than by a new status).
//  3. The user answers via POST /runs/:id/clarify. The API handler persists the clarification as a
//     durable semantic-layer Correction (so the same ambiguity is not re-asked in the same context —
//     the correction rides into every later payload's grounding) and hands the answer to
//     Clarify, which writes it into the plan step's output and resumes the pipeline from generate_sql.
//
// The plan output shape deliberately EXTENDS domain.PlanStepOutput rather than editing the shared
// domain type: planOutput embeds it (so existing fields behave identically) and adds the
// clarification fields as pure additions. The persisted step output therefore carries both the
// original answerable/reason/approach contract and the new uncertainty surface.

// planOutput is the parsed shape of the "plan" step's Step.Output as this executor writes it:
// domain.PlanStepOutput plus the FEAT-033 clarification fields. Embedding (rather than editing the
// shared type) keeps the original contract intact while the new fields are purely additive.
type planOutput struct {
	domain.PlanStepOutput
	// NeedsClarification is true when the question is ambiguous and the run must pause for a
	// specific clarifying question rather than proceed on a guess (FEAT-033).
	NeedsClarification bool `json:"needs_clarification,omitempty"`
	// ClarificationQuestion is the SPECIFIC question resolving the actual ambiguity, e.g. "Do you
	// mean revenue by total value or by order count?" — never a generic "please clarify".
	ClarificationQuestion string `json:"clarification_question,omitempty"`
	// ClarificationOptions is a small enumerable set of discrete resolutions when the ambiguity has
	// one, e.g. ["By revenue", "By count"]. Empty when a free-text answer is the honest shape.
	ClarificationOptions []string `json:"clarification_options,omitempty"`
	// AmbiguityKey is a stable short key naming the ambiguity (e.g. "metric_undefined"), used with
	// the persisted clarification correction so a functionally identical follow-up in the same
	// context is not re-asked (FEAT-033 AC3).
	AmbiguityKey string `json:"ambiguity_key,omitempty"`
	// ClarificationAnswer is the user's answer, written into the plan output when the clarify
	// endpoint resumes the run. The plan step replays with it on resume, and generate_sql receives
	// it as prior context.
	ClarificationAnswer string `json:"clarification_answer,omitempty"`
}

// clarificationResolution returns the user's clarification answer (trimmed), or "" when the plan
// has not been clarified yet.
func (p planOutput) clarificationResolution() string {
	return strings.TrimSpace(p.ClarificationAnswer)
}

// awaitingClarification reports whether the plan output represents a pending, unanswered
// clarifying question — the state the Run Viewer renders screen 12's prompt for.
func (p planOutput) awaitingClarification() bool {
	return p.NeedsClarification && p.clarificationResolution() == ""
}

// Clarify resumes a run that paused for a clarifying question (FEAT-033): the user's answer is
// written into the plan step's persisted output, then the pipeline resumes from generate_sql with
// the clarification as prior context. It returns immediately; execution runs in the background like
// every other executor entry point.
func (e *Executor) Clarify(userID, runID, answer string) {
	ctx, cancel := e.runContext(runID)
	go func() {
		defer cancel()
		defer e.releaseRunContext(runID)
		e.clarifyAndResume(ctx, userID, runID, answer)
	}()
}

// clarifyAndResume is Clarify's background half. It guards every assumption: the run must exist, be
// halted (the paused state), and its plan step must carry a PENDING clarification. Any guard that
// fails logs and returns — the run stays paused rather than being resumed into a state the answer
// does not belong to.
func (e *Executor) clarifyAndResume(ctx context.Context, userID, runID, answer string) {
	run, err := e.runs.GetByID(ctx, runID)
	if err != nil {
		e.logger.Error("clarifying run: loading", slog.String("run_id", runID), slog.String("error", err.Error()))
		return
	}
	log := e.logger.With(slog.String("run_id", run.ID), slog.String("workspace_id", run.WorkspaceID))

	if run.Status != domain.RunHalted {
		log.Warn("clarify: run is not paused awaiting clarification", slog.String("status", string(run.Status)))
		return
	}

	steps, err := e.steps.ListByRun(ctx, run.ID)
	if err != nil {
		e.logger.Error("clarifying run: listing steps", slog.String("error", err.Error()))
		return
	}
	var planStep *domain.Step
	for i := range steps {
		if steps[i].StepName == domain.StepPlan {
			planStep = &steps[i]
			break
		}
	}
	if planStep == nil || planStep.Status != domain.StepComplete {
		log.Warn("clarify: no completed plan step to clarify")
		return
	}

	var out planOutput
	if err := json.Unmarshal([]byte(planStep.Output), &out); err != nil {
		e.logger.Error("clarifying run: parsing plan output", slog.String("error", err.Error()))
		return
	}
	if !out.awaitingClarification() {
		log.Warn("clarify: plan step does not carry a pending clarifying question")
		return
	}

	// Persist the answer into the plan step's output. Complete is used deliberately: StepRepository's
	// Complete has no "not already complete" guard, so re-completing the SAME plan row with the
	// answer is the one existing store call that rewrites a completed row's output in place — the
	// row and its UNIQUE idempotency key are untouched, and the step machine replays the enriched
	// output on resume.
	out.ClarificationAnswer = strings.TrimSpace(answer)
	encoded, err := json.Marshal(out)
	if err != nil {
		e.logger.Error("clarifying run: encoding plan output", slog.String("error", err.Error()))
		return
	}
	if err := e.steps.Complete(ctx, planStep.ID, string(encoded)); err != nil {
		e.logger.Error("clarifying run: writing clarification into plan step", slog.String("error", err.Error()))
		return
	}

	// Resume from plan: the completed plan step replays (now carrying the answer), generate_sql and
	// everything after it run fresh with the clarification as prior context.
	e.executePipeline(ctx, userID, run, nil, true, strings.TrimSpace(answer))
}

// clarificationCacheSeed folds a clarification answer into a determinism-cache seed so a clarified
// execution never replays (or shadows) an unclarified run of the same question, and vice versa.
func clarificationCacheSeed(answer string) string {
	if strings.TrimSpace(answer) == "" {
		return ""
	}
	return "clarify:" + strings.TrimSpace(answer)
}
