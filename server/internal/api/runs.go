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
type RunExecutor interface {
	Submit(userID string, run domain.Run)
}

// RegisterRunRoutes wires FEAT-013's endpoints onto r.
func RegisterRunRoutes(r chi.Router, workspaces WorkspaceStore, runs RunStore, steps RunStepStore, executor RunExecutor, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/runs", handleCreateRun(workspaces, runs, executor, logger))
	r.Get("/workspaces/{workspaceId}/runs/{runId}", handleGetRun(workspaces, runs, steps, logger))
}

// createRunRequest is the POST body for asking a question: screens/04-workspace-home.md's Ask panel.
type createRunRequest struct {
	QuestionText string `json:"question_text"`
}

// handleCreateRun serves screens/04-workspace-home.md's Ask panel: POST /api/workspaces/:id/runs.
//
// The run is created in "queued" status and handed to the executor, which returns immediately — the
// 202 Accepted response is the honest one for "accepted, processing in the background", and matches
// architecture.md's "runs are server-side state; the UI is a viewer attached to them" framing: the
// user navigates to the Run Viewer knowing the run may not have progressed past queued yet.
func handleCreateRun(workspaces WorkspaceStore, runs RunStore, executor RunExecutor, logger *slog.Logger) http.HandlerFunc {
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

		executor.Submit(claims.UserID, run)

		respondJSON(w, http.StatusAccepted, map[string]any{"run": run})
	}
}

// handleGetRun serves screens/08-run-viewer.md's polling endpoint: GET /api/workspaces/:id/runs/:runId.
//
// The viewer polls this every ~1s while the run is live; returning run and steps together means one
// round trip renders both the status pill and the step feed consistently.
func handleGetRun(workspaces WorkspaceStore, runs RunStore, steps RunStepStore, logger *slog.Logger) http.HandlerFunc {
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

		respondJSON(w, http.StatusOK, map[string]any{
			"run":   run,
			"steps": stepList,
		})
	}
}
