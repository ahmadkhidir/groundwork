package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
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
type RunExecutor interface {
	Submit(userID string, run domain.Run)
	SubmitWithSQL(userID string, run domain.Run, sql string)
}

// RegisterRunRoutes wires FEAT-013's endpoints onto r. reports backs the GET run response's
// report_id lookup (FEAT-018); it may be nil in test doubles that do not exercise that field.
//
// corrections backs FEAT-020's capture hook: when a run is created with the user's edited SQL
// (FEAT-018), handleCreateRun persists a durable Correction row attributing the edit. It is the same
// SemanticCorrectionStore the semantic layer routes use (api.go's SemanticCorrections seam) and may
// be nil when the semantic layer is not wired (e.g. minimal run-route tests) — capture is skipped,
// never an error.
func RegisterRunRoutes(r chi.Router, workspaces WorkspaceStore, runs RunStore, steps RunStepStore, reports ReportStore, corrections SemanticCorrectionStore, executor RunExecutor, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/runs", handleCreateRun(workspaces, runs, corrections, executor, logger))
	r.Get("/workspaces/{workspaceId}/runs/{runId}", handleGetRun(workspaces, runs, steps, reports, logger))
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
