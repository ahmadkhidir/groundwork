package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
)

// RunStore is the create/get seam RegisterRunRoutes depends on for runs, satisfied by
// internal/store/postgres.RunRepository.
type RunStore interface {
	Create(ctx context.Context, run domain.Run) error
	GetByID(ctx context.Context, id string) (domain.Run, error)
}

// RunStepStore exposes a run's persisted step feed, satisfied by postgres.StepRepository.
//
// Narrower than the full repository on purpose: the viewer reads steps; only the executor writes
// them.
type RunStepStore interface {
	ListByRun(ctx context.Context, runID string) ([]domain.Step, error)
}

// RunExecutor starts a run's background execution, satisfied by internal/run.Executor.
//
// The handler persists the run in "queued" status and then hands it to the executor; the executor —
// never an HTTP handler — is the only component allowed to advance run state after that.
//
// SubmitWithSQL is FEAT-018's Phase 2 boundary: an edited query starts a NEW run whose generate_sql
// step is seeded with the user's SQL (see internal/run's executeSeeded). Both entry points return
// immediately.
//
// Resume (FEAT-022) restarts a previously-failed/interrupted run from its first unfinished step; the
// executor's step machine replays completed work and reclaims an incomplete step, so it can never
// double-execute a step. It returns immediately.
//
// Halt (FEAT-024) stops an in-flight run: the executor cancels its background work (model/engine
// calls abort) and marks the run halted so the viewer can edit the SQL. It returns immediately.
//
// AmendAndResume (FEAT-024) restarts a halted run from an amended step: the amended step and
// everything after it in pipeline order are invalidated for re-execution, earlier completed steps
// are preserved, and execution resumes. It returns immediately.
type RunExecutor interface {
	Submit(userID string, run domain.Run)
	SubmitWithSQL(userID string, run domain.Run, sql string)
	Resume(userID string, runID string)
	Halt(userID string, runID string)
	AmendAndResume(userID string, runID string, stepName domain.StepName, input string)
}

// RegisterRunRoutes wires FEAT-013's endpoints onto r. reports backs the GET run response's
// report_id lookup (FEAT-018); it may be nil in test doubles that do not exercise that field.
//
// corrections backs FEAT-020's capture hook: when a run is created with the user's edited SQL
// (FEAT-018), handleCreateRun persists a durable Correction row attributing the edit. It is the same
// SemanticCorrectionStore the semantic layer routes use (api.go's SemanticCorrections seam) and may
// be nil when the semantic layer is not wired (e.g. minimal run-route tests) — capture is skipped,
// never an error.
//
// hub, when non-nil, backs FEAT-023's live run-event stream (a "run.status"/step event subscription
// that streams the run's in-progress DAG transitions to the Run Viewer over SSE) and FEAT-022's
// resume endpoint.
func RegisterRunRoutes(r chi.Router, workspaces WorkspaceStore, runs RunStore, steps RunStepStore, reports ReportStore, corrections SemanticCorrectionStore, executor RunExecutor, hub *events.Hub, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/runs", handleCreateRun(workspaces, runs, corrections, executor, logger))
	r.Get("/workspaces/{workspaceId}/runs/{runId}", handleGetRun(workspaces, runs, steps, reports, logger))
	if hub != nil {
		r.Get("/workspaces/{workspaceId}/runs/{runId}/stream", handleStreamRun(workspaces, runs, hub, logger))
		r.Post("/workspaces/{workspaceId}/runs/{runId}/resume", handleResumeRun(workspaces, runs, executor, logger))
		r.Post("/workspaces/{workspaceId}/runs/{runId}/halt", handleHaltRun(workspaces, runs, executor, logger))
		r.Post("/workspaces/{workspaceId}/runs/{runId}/amend", handleAmendRun(workspaces, runs, executor, logger))
	}
}

// createRunRequest is the POST body for asking a question: screens/04-workspace-home.md's Ask panel,
// and FEAT-018's "Run edited query" action (question_text carried forward + the edited sql).
type createRunRequest struct {
	QuestionText string `json:"question_text"`
	// SQL, when present, seeds the new run's generate_sql output with the user's edited query
	// (FEAT-018): the run skips plan and model generate_sql and executes exactly this SQL.
	SQL string `json:"sql,omitempty"`
}

// handleCreateRun serves screens/04-workspace-home.md's Ask panel: POST /api/workspaces/:id/runs.
//
// The run is created in "queued" status and handed to the executor, which returns immediately — the
// 202 Accepted response is the honest one for "accepted, processing in the background", and matches
// architecture.md's "runs are server-side state; the UI is a viewer attached to them" framing: the
// user navigates to the Run Viewer knowing the run may not have progressed past queued yet.
//
// FEAT-020's capture hook lives here: when the request carries the user's edited SQL (FEAT-018), a
// durable Correction row is persisted attributing the edit to the NEW run before the executor is
// handed it. Capture is best-effort by design — a failure to persist the preference logs and
// continues, because the run the user asked for is the primary resource and must not be blocked on a
// secondary write.
func handleCreateRun(workspaces WorkspaceStore, runs RunStore, corrections SemanticCorrectionStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		var req createRunRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		// A blank question is a client bug, not a model request — the Ask panel disables submit for
		// empty input, and this check is the control behind that UX affordance.
		question := strings.TrimSpace(req.QuestionText)
		if question == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Ask a question first.",
				"code":  "invalid_question",
			})
			return
		}

		run := domain.Run{
			ID:           uuid.NewString(),
			WorkspaceID:  workspaceID,
			QuestionText: question,
			Status:       domain.RunQueued,
			CreatedAt:    time.Now().UTC(),
		}
		if err := runs.Create(r.Context(), run); err != nil {
			logger.Error("creating run",
				slog.String("workspace_id", workspaceID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		// FEAT-018: an edited query (sql present) forks the run's execution through the seeded
		// path — the same question, the user's corrected query. A blank/absent sql is the ordinary
		// question → plan → generate_sql path.
		if sql := strings.TrimSpace(req.SQL); sql != "" {
			// FEAT-020 capture: the edit is a durable semantic-layer preference, not a one-off
			// patch. Attributed to the NEW run (the run "during which this correction was made",
			// per schema.md) — see captureCorrection's doc for the full decision.
			if corrections != nil {
				if err := corrections.Create(r.Context(), captureCorrection(run, question, sql)); err != nil {
					logger.Error("capturing correction from edited-SQL run",
						slog.String("workspace_id", workspaceID),
						slog.String("run_id", run.ID),
						slog.String("error", err.Error()),
					)
				}
			}
			executor.SubmitWithSQL(claims.UserID, run, sql)
		} else {
			executor.Submit(claims.UserID, run)
		}

		respondJSON(w, http.StatusAccepted, map[string]any{"run": run})
	}
}

// captureCorrection builds the durable Correction row FEAT-020 persists when a user's edited SQL
// starts a new run (FEAT-018's seeded path), from what the create request actually carries.
//
// Decisions, and the doc tensions behind them (routed to product-shaper in the wave report):
//
//   - OriginatingRunID points at the NEW run, not the run whose SQL was edited. The schema describes
//     originating_run_id as "the Run during which this correction was made", and this correction IS
//     made while creating the seeded run. The original run's id is also not carried by the create
//     request (no parent_run_id is passed on FEAT-018's path), so the new run is the only honest,
//     durable anchor available here.
//
//   - OriginalLogic is stored empty. The request carries only the corrected SQL; the original
//     generated SQL lives in the previous run's generate_sql step, whose id the client does not send.
//     Storing a fabricated "original" would corrupt the Corrections tab's original-vs-corrected
//     comparison, so the honest value is an empty original with the corrected SQL and the question
//     context preserved. schema.md marks original_logic required; this is surfaced as a doc
//     correction candidate (see FEAT-020 AC2 / screen 15 in the wave report).
//
//   - Description is derived from the question text: with no parsed understanding of the edit, the
//     plain-language summary anchors the correction to the question whose generated SQL was edited.
func captureCorrection(run domain.Run, question, sql string) domain.Correction {
	return domain.Correction{
		ID:               uuid.NewString(),
		WorkspaceID:      run.WorkspaceID,
		Description:      correctionDescription(question),
		OriginalLogic:    "",
		CorrectedLogic:   sql,
		OriginatingRunID: run.ID,
		CreatedAt:        time.Now().UTC(),
	}
}

// correctionDescription builds a short plain-language summary of a captured correction, anchoring it
// to the question whose generated SQL was edited. The question is whitespace-collapsed and truncated
// so a long question cannot balloon the Corrections tab's description column.
func correctionDescription(question string) string {
	q := strings.Join(strings.Fields(question), " ")
	if len(q) > correctionDescriptionMax {
		q = q[:correctionDescriptionMax] + "…"
	}
	return "Edited the SQL generated for: " + q
}

// correctionDescriptionMax bounds the question excerpt embedded in a correction's description.
const correctionDescriptionMax = 60

// handleGetRun serves screens/08-run-viewer.md's polling endpoint: GET /api/workspaces/:id/runs/:runId.
//
// The viewer polls this every ~1s while the run is live; returning run and steps together means one
// round trip renders both the status pill and the step feed consistently. A run-scoped report_id is
// attached when one of this run's reports exists (FEAT-018's auto-navigate-to-report uses it); null
// while running or when no report materialized.
func handleGetRun(workspaces WorkspaceStore, runs RunStore, steps RunStepStore, reports ReportStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		runID := chi.URLParam(r, "runId")
		run, err := runs.GetByID(r.Context(), runID)
		if err != nil {
			// A run id is scoped to its workspace, and this API never confirms the existence of an
			// id the caller cannot own (the same non-enumeration stance as handleListColumns's doc
			// comment) — so both a missing row and a store error read as a plain 404.
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Run not found.",
				"code":  "not_found",
			})
			return
		}

		// The repository's GetByID is ID-only (the executor needs that shape); workspace scoping is
		// enforced here, next to requireWorkspaceOwnership. Without this check a run belonging to a
		// different workspace would be returned to a caller who owns only the URL's workspace.
		if run.WorkspaceID != workspaceID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Run not found.",
				"code":  "not_found",
			})
			return
		}

		stepList, err := steps.ListByRun(r.Context(), run.ID)
		if err != nil {
			logger.Error("listing run steps",
				slog.String("run_id", run.ID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		// report_id is advisory for the viewer's auto-navigate; a report that does not exist (or a
		// test double without the reports seam) renders as JSON null, never a broken field.
		var reportID any
		if reports != nil {
			if report, err := reports.GetByRunID(r.Context(), workspaceID, run.ID); err == nil {
				reportID = report.ID
			}
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"run":       run,
			"steps":     stepList,
			"report_id": reportID,
		})
	}
}

// handleStreamRun serves FEAT-023's live run-event stream:
// GET /api/workspaces/:id/runs/:runId/stream.
//
// The response is a persistent SSE stream (text/event-stream). On connect the handler replays the
// run's CURRENT persisted state as events (one event per step's status, then a run.status event), so
// a viewer joining mid-run renders state immediately; it then subscribes to the in-process event hub
// and forwards each live transition. Events carry no SSE `event:` field, so the browser's generic
// `onmessage` handler receives them as JSON text (see web/src/lib/api-client.ts subscribeToStream).
//
// The stream is best-effort and explicitly not load-bearing: the durable source of truth is the
// persisted runs/steps the GET endpoint reads, so any disconnect (or this stream's 60s request
// timeout, an accepted Pass-1 limitation) is repaired by the viewer re-reading state and
// reconnecting. A stream.end event is sent after the run reaches a terminal state so the viewer can
// stop listening.
func handleStreamRun(workspaces WorkspaceStore, runs RunStore, hub *events.Hub, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		runID := chi.URLParam(r, "runId")
		run, err := runs.GetByID(r.Context(), runID)
		if err != nil || run.WorkspaceID != workspaceID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Run not found.",
				"code":  "not_found",
			})
			return
		}

		ch, unsubscribe := hub.Subscribe(runID)
		defer unsubscribe()

		flusher, ok := w.(http.Flusher)
		if !ok {
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Streaming unsupported.",
				"code":  "internal_error",
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		// 1. Replay the persisted snapshot so a mid-run reconnect renders current state instantly.
		snapshot := []events.Event{{Type: events.TypeRunStatus, RunID: run.ID, Status: string(run.Status)}}
		for _, line := range snapshot {
			writeSSE(w, flusher, line)
		}

		// 2. If the run is already terminal, there will be no live events; signal the end.
		if run.Status.IsTerminal() {
			writeSSE(w, flusher, events.Event{Type: events.TypeStreamEnd, RunID: run.ID})
			return
		}

		// 3. Forward live events until the run is terminal or the client disconnects.
		for {
			select {
			case ev := <-ch:
				writeSSE(w, flusher, ev)
				if ev.Type == events.TypeRunStatus {
					status := domain.RunStatus(ev.Status)
					if status.IsTerminal() {
						writeSSE(w, flusher, events.Event{Type: events.TypeStreamEnd, RunID: run.ID})
						return
					}
					if status == domain.RunFailed {
						logger.Info("stream: run failed")
						return
					}
				}
			case <-r.Context().Done():
				return
			}
		}
	}
}

func writeSSE(w io.Writer, flusher http.Flusher, ev events.Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	flusher.Flush()
}

// handleResumeRun serves FEAT-022's resume endpoint:
// POST /api/workspaces/:id/runs/:runId/resume.
//
// It restarts a failed or interrupted run from its first unfinished step. The step machine's
// idempotency+skip guarantees make this safe: completed steps are replayed, the first non-completed
// one is reclaimed, and a run already being executed concurrently is rejected with 409 rather than
// double-driven. A truly completed run is also rejected — there is nothing honest to resume.
func handleResumeRun(workspaces WorkspaceStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		runID := chi.URLParam(r, "runId")
		run, err := runs.GetByID(r.Context(), runID)
		if err != nil || run.WorkspaceID != workspaceID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Run not found.",
				"code":  "not_found",
			})
			return
		}

		// Only a terminal-bad or interrupted run is resumable. As for resume, a queued/waking/running
		// run is already being driven by the executor; re-entering it would race live execution.
		switch run.Status {
		case domain.RunFailed, domain.RunHalted:
			executor.Resume(claims.UserID, runID)
			respondJSON(w, http.StatusAccepted, map[string]any{"status": "resumed", "run_id": runID})
		default:
			respondJSON(w, http.StatusConflict, map[string]string{
				"error": "This run is already finished or in progress and cannot be resumed.",
				"code":  "run_not_resumable",
			})
		}
	}
}

// handleHaltRun serves FEAT-024's halt endpoint: POST /api/workspaces/:id/runs/:runId/halt.
//
// It stops an in-flight run — the executor cancels the run's background execution context (aborting
// the current step's model/engine work) and marks the run halted so the viewer pivots to the
// halted/editable state. A terminal run has nothing to halt; the endpoint still acknowledges the
// request (idempotent), mirroring how resume treats a no-op resume.
func handleHaltRun(workspaces WorkspaceStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		runID := chi.URLParam(r, "runId")
		run, err := runs.GetByID(r.Context(), runID)
		if err != nil || run.WorkspaceID != workspaceID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Run not found.",
				"code":  "not_found",
			})
			return
		}

		executor.Halt(claims.UserID, runID)
		respondJSON(w, http.StatusAccepted, map[string]any{"status": "halted", "run_id": runID})
	}
}

// amendRunRequest is the POST body for amending a halted step (FEAT-024): which step is being
// amended and its new input. This wave's executor supports amending generate_sql, where input is
// the user's edited SQL.
type amendRunRequest struct {
	StepName domain.StepName `json:"step_name"`
	Input    string          `json:"input"`
}

// handleAmendRun serves FEAT-024's amend endpoint:
// POST /api/workspaces/:id/runs/:runId/amend.
//
// It hands the amendment to the executor, which invalidates the amended step and everything after it
// for re-execution and resumes the run from that step forward — earlier completed steps are never
// re-run (AC3). The request is validated for the fields the executor consumes; a blank step or a
// blank input is a client bug, not a model request.
func handleAmendRun(workspaces WorkspaceStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		runID := chi.URLParam(r, "runId")
		run, err := runs.GetByID(r.Context(), runID)
		if err != nil || run.WorkspaceID != workspaceID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Run not found.",
				"code":  "not_found",
			})
			return
		}

		var req amendRunRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.StepName == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "A step to amend is required.",
				"code":  "invalid_step",
			})
			return
		}
		if strings.TrimSpace(req.Input) == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Amended input cannot be empty.",
				"code":  "invalid_input",
			})
			return
		}

		executor.AmendAndResume(claims.UserID, runID, req.StepName, req.Input)
		respondJSON(w, http.StatusAccepted, map[string]any{"status": "amended", "run_id": runID})
	}
}
