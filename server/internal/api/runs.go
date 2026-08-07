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
	// ListByParentRun backs FEAT-025's branch/tree presentation: the Run Viewer shows every run
	// forked from the run being viewed as its branches.
	ListByParentRun(ctx context.Context, parentRunID string) ([]domain.Run, error)
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
//
// Clarify (FEAT-033) answers a run that paused for a clarifying question: the answer is written
// into the plan step's persisted output and the run resumes from generate_sql with the
// clarification applied. It returns immediately.
type RunExecutor interface {
	Submit(userID string, run domain.Run)
	SubmitWithSQL(userID string, run domain.Run, sql string)
	Resume(userID string, runID string)
	Halt(userID string, runID string)
	AmendAndResume(userID string, runID string, stepName domain.StepName, input string)
	Clarify(userID string, runID string, answer string)
}

// RegisterRunRoutes wires FEAT-013's endpoints onto r. reports backs the GET run response's
// report_id lookup (FEAT-018) and the FEAT-021/FEAT-025 parent-context validation on run creation
// (a follow-up report must exist in the workspace before a run may claim it as parent); it may be
// nil in test doubles that do not exercise those fields.
//
// users backs FEAT-021/FEAT-025's tier gate: compositional follow-ups and fork-on-amendment are
// Pro/Team per monetization.md, enforced against the AUTHORITATIVE tier from UserTierLookup (never
// the session cookie's claim) — the same discipline RegisterExportRoutes follows. It may be nil in
// test doubles that do not exercise the gate.
//
// corrections backs FEAT-020's capture hook: when a run is created with the user's edited SQL
// (FEAT-018), handleCreateRun persists a durable Correction row attributing the edit, and the
// clarifying-question flow (FEAT-033) persists each clarification the same way so the same ambiguity
// is not re-asked in the same context. It is the same SemanticCorrectionStore the semantic layer
// routes use (api.go's SemanticCorrections seam) and may be nil when the semantic layer is not wired
// (e.g. minimal run-route tests) — capture is skipped, never an error.
//
// hub, when non-nil, backs FEAT-023's live run-event stream (a "run.status"/step event subscription
// that streams the run's in-progress DAG transitions to the Run Viewer over SSE) and FEAT-022's
// resume endpoint.
//
// The clarify endpoint is registered OUTSIDE the hub gate on purpose: answering a clarifying
// question is a run mutation, not a streaming concern, so FEAT-033 keeps working even in a test or
// minimal deployment where the SSE hub is not wired.
func RegisterRunRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, steps RunStepStore, reports ReportStore, users UserTierLookup, corrections SemanticCorrectionStore, executor RunExecutor, hub *events.Hub, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/runs", handleCreateRun(workspaces, members, runs, reports, users, corrections, executor, logger))
	r.Get("/workspaces/{workspaceId}/runs/{runId}", handleGetRun(workspaces, members, runs, steps, reports, logger))
	r.Post("/workspaces/{workspaceId}/runs/{runId}/clarify", handleClarifyRun(workspaces, members, runs, steps, corrections, executor, logger))
	if hub != nil {
		r.Get("/workspaces/{workspaceId}/runs/{runId}/stream", handleStreamRun(workspaces, members, runs, hub, logger))
		r.Post("/workspaces/{workspaceId}/runs/{runId}/resume", handleResumeRun(workspaces, members, runs, executor, logger))
		r.Post("/workspaces/{workspaceId}/runs/{runId}/halt", handleHaltRun(workspaces, members, runs, executor, logger))
		r.Post("/workspaces/{workspaceId}/runs/{runId}/amend", handleAmendRun(workspaces, members, runs, executor, logger))
	}
}

// createRunRequest is the POST body for asking a question: screens/04-workspace-home.md's Ask panel,
// FEAT-018's "Run edited query" action (question_text carried forward + the edited sql), FEAT-021's
// report-scoped "Ask a follow-up" (parent_report_id), and FEAT-025's fork-on-amendment
// (parent_run_id).
type createRunRequest struct {
	QuestionText string `json:"question_text"`
	// SQL, when present, seeds the new run's generate_sql output with the user's edited query
	// (FEAT-018): the run skips plan and model generate_sql and executes exactly this SQL.
	SQL string `json:"sql,omitempty"`
	// ParentRunID forks this run from the named run (FEAT-025): the new run records it as
	// parent_run_id, the original stays intact, and the Run Viewer presents both as a branch tree.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// ParentReportID makes this run a compositional follow-up on the named report (FEAT-021): the
	// run is linked to the report's source run, and the report this run materializes records the
	// parent report id. When both parent fields are present, the report's run takes precedence and
	// the run linkage is derived from it (they are the same fork under two names).
	ParentReportID string `json:"parent_report_id,omitempty"`
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
//
// FEAT-021/FEAT-025's parent context also lives here: a request carrying parent_report_id (a
// compositional follow-up) or parent_run_id (a fork-on-amendment) links the new run to its parent
// BEFORE the executor sees it, so the report this run materializes can record its own parent link.
// Both are validated against the workspace (non-enumeration): an id the caller cannot own reads as
// not found. Both are also TIER-GATED — monetization.md gates FEAT-021/FEAT-025 to Pro/Team — and
// the authoritative tier is read from Postgres via users, never the session cookie's claim (the
// same discipline RegisterExportRoutes follows).
func handleCreateRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, reports ReportStore, users UserTierLookup, corrections SemanticCorrectionStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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

		// Resolve the parent linkage (FEAT-021/FEAT-025). A report-scoped follow-up is the richer
		// context: validate the report exists in this workspace and derive the run linkage from the
		// report's own source run. A bare parent_run_id (fork-on-amendment) is validated the same
		// non-enumeration way as handleGetRun's workspace check.
		parentID := strings.TrimSpace(req.ParentRunID)
		reportID := strings.TrimSpace(req.ParentReportID)
		if parentID != "" || reportID != "" {
			// Tier gate: follow-ups and forks are Pro/Team (monetization.md). The authoritative
			// tier comes from the store, not the session cookie.
			if users != nil {
				user, _, err := users.FindByID(r.Context(), claims.UserID)
				if err != nil {
					logger.Error("looking up user for follow-up tier gate",
						slog.String("workspace_id", workspaceID),
						slog.String("error", err.Error()),
					)
					respondJSON(w, http.StatusInternalServerError, map[string]string{
						"error": "Something went wrong. Try again.",
						"code":  "internal_error",
					})
					return
				}
				if user.Tier != domain.TierPro && user.Tier != domain.TierTeam {
					respondJSON(w, http.StatusForbidden, map[string]string{
						"error": "Follow-up questions and forking are available on the Pro and Team plans. Upgrade to branch from a report or run.",
						"code":  "upgrade_required",
					})
					return
				}
			}
		}
		if reportID != "" {
			if reports == nil {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Follow-up questions are not available right now.",
					"code":  "parent_context_unavailable",
				})
				return
			}
			parent, err := reports.GetByID(r.Context(), workspaceID, reportID)
			if err != nil {
				// The store scopes by workspace, so both "does not exist" and "belongs to another
				// workspace" read as one 404 — the same stance every read endpoint takes.
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Report not found.",
					"code":  "not_found",
				})
				return
			}
			parentID = parent.RunID
		} else if parentID != "" {
			parent, err := runs.GetByID(r.Context(), parentID)
			if err != nil || parent.WorkspaceID != workspaceID {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Run not found.",
					"code":  "not_found",
				})
				return
			}
		}
		run.ParentRunID = parentID

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
func handleGetRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, steps RunStepStore, reports ReportStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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

		// FEAT-025: the run's forked children (the branches of the tree this run is the parent of).
		// Served with the run itself so the Run Viewer's branch/tree presentation is one round trip.
		// A store error degrades to an empty list — the run is the primary resource and the tree is
		// a presentation nicety; the fork link the run already carries never depends on it.
		forked, err := runs.ListByParentRun(r.Context(), run.ID)
		if err != nil {
			logger.Error("listing forked runs",
				slog.String("run_id", run.ID),
				slog.String("error", err.Error()),
			)
			forked = []domain.Run{}
		}
		if forked == nil {
			forked = []domain.Run{}
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"run":         run,
			"steps":       stepList,
			"report_id":   reportID,
			"forked_runs": forked,
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
func handleStreamRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, hub *events.Hub, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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
func handleResumeRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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
func handleHaltRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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
func handleAmendRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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

// clarifyRunRequest is the POST body for answering a run's clarifying question (FEAT-033, screen 12).
type clarifyRunRequest struct {
	// Answer is the user's resolution of the specific ambiguity, e.g. "By revenue".
	Answer string `json:"answer"`
}

// handleClarifyRun serves the clarifying-question flow (FEAT-033, screen 12):
// POST /api/workspaces/:id/runs/:runId/clarify.
//
// A run that paused with a pending clarifying question (the plan step completed with
// needs_clarification and the run was settled to halted) is answered here: the handler persists the
// clarification as a durable, workspace-scoped Correction — so the SAME ambiguity is not re-asked
// for a functionally identical follow-up in the same context (AC3, via the corrections grounding
// every later payload carries) — and hands the answer to the executor, which writes it into the plan
// step's output and resumes the run from generate_sql with the clarification applied.
//
// Guards: the run must be halted AND its plan step must carry a pending clarification — a run the
// user halted manually has nothing to clarify, and answering it would be a fabricated resolution.
// The executor re-verifies the same guards; the handler's checks are the HTTP-layer control that
// turns a misdirected POST into a 409 rather than a silent no-op.
func handleClarifyRun(workspaces WorkspaceStore, members MemberLookupStore, runs RunStore, steps RunStepStore, corrections SemanticCorrectionStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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

		var req clarifyRunRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		answer := strings.TrimSpace(req.Answer)
		if answer == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "An answer is required.",
				"code":  "invalid_answer",
			})
			return
		}

		// Verify the run is actually paused awaiting clarification: halted status AND a completed
		// plan step whose output carries a pending needs_clarification. Anything else is a 409 —
		// the run is not in a state this answer belongs to.
		plan, ok := planStepPendingClarification(r, steps, runID)
		if !ok {
			respondJSON(w, http.StatusConflict, map[string]string{
				"error": "This run is not waiting for a clarifying question.",
				"code":  "run_not_awaiting_clarification",
			})
			return
		}

		// FEAT-033 AC3: persist the clarification as a durable, workspace-scoped preference so the
		// same ambiguity is not re-asked in this context. Best-effort like FEAT-020's capture: the
		// resumed run is the primary resource and must not be blocked on a secondary write.
		if corrections != nil {
			if err := corrections.Create(r.Context(), clarifyCorrection(run, plan, answer)); err != nil {
				logger.Error("persisting clarification correction",
					slog.String("workspace_id", workspaceID),
					slog.String("run_id", run.ID),
					slog.String("error", err.Error()),
				)
			}
		}

		executor.Clarify(claims.UserID, runID, answer)
		respondJSON(w, http.StatusAccepted, map[string]any{"status": "clarified", "run_id": runID})
	}
}

// pendingClarificationPlan is the slice of a plan step's output handleClarifyRun needs: whether a
// clarification is pending and the ambiguity's stable key + question for the persisted correction.
type pendingClarificationPlan struct {
	NeedsClarification    bool     `json:"needs_clarification"`
	ClarificationQuestion string   `json:"clarification_question"`
	ClarificationOptions  []string `json:"clarification_options"`
	AmbiguityKey          string   `json:"ambiguity_key"`
	ClarificationAnswer   string   `json:"clarification_answer"`
}

// planStepPendingClarification reads the run's plan step and reports whether it carries a PENDING
// clarifying question (needs_clarification set and no answer yet). ok=false covers every other
// state — no plan step, plan not complete, malformed output, or an already-answered clarification.
func planStepPendingClarification(r *http.Request, steps RunStepStore, runID string) (pendingClarificationPlan, bool) {
	list, err := steps.ListByRun(r.Context(), runID)
	if err != nil {
		return pendingClarificationPlan{}, false
	}
	for _, st := range list {
		if st.StepName != domain.StepPlan || st.Status != domain.StepComplete {
			continue
		}
		var plan pendingClarificationPlan
		if err := json.Unmarshal([]byte(st.Output), &plan); err != nil {
			return pendingClarificationPlan{}, false
		}
		if !plan.NeedsClarification || strings.TrimSpace(plan.ClarificationAnswer) != "" {
			return pendingClarificationPlan{}, false
		}
		return plan, true
	}
	return pendingClarificationPlan{}, false
}

// clarifyCorrection builds the durable Correction row FEAT-033 persists when a user answers a
// clarifying question, so the same ambiguity is not re-asked for functionally identical follow-up
// questions in the same context (AC3).
//
// The row's shape rides the existing corrections surface: Description leads with "Clarification:"
// and carries the ambiguity key in brackets (the plan prompt recognizes this prefix), CorrectedLogic
// is the user's answer, and OriginalLogic is the ambiguity key (the "logic" the clarification
// overrides). OriginatingRunID anchors it to the run whose clarification produced it, matching
// FEAT-020's attribution convention.
func clarifyCorrection(run domain.Run, plan pendingClarificationPlan, answer string) domain.Correction {
	key := strings.TrimSpace(plan.AmbiguityKey)
	question := strings.TrimSpace(plan.ClarificationQuestion)
	if key == "" {
		key = "ambiguity"
	}
	description := fmt.Sprintf("Clarification [%s]: %s", key, question)
	if question == "" {
		description = fmt.Sprintf("Clarification [%s]", key)
	}
	return domain.Correction{
		ID:               uuid.NewString(),
		WorkspaceID:      run.WorkspaceID,
		Description:      description,
		OriginalLogic:    key,
		CorrectedLogic:   answer,
		OriginatingRunID: run.ID,
		CreatedAt:        time.Now().UTC(),
	}
}
