package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// EngineStatusProvider is the seam RegisterEngineRoutes depends on for reading a workspace's LIVE
// engine state, satisfied by internal/engine.Provider (engine.LocalProvider this wave — see
// architecture.md's "implementation #1" framing; a future remote/pooled implementation behind the
// same seam swaps in without this handler changing).
//
// Deliberately narrower than the full engine.Provider interface: this handler only ever reads
// status, and never acquires or releases an engine — Status's own contract is that reading it must
// never itself trigger a wake, so this seam makes it impossible for this handler to accidentally do
// so by calling the wrong method.
type EngineStatusProvider interface {
	Status(ctx context.Context, workspaceID string) (engine.Status, error)
}

// RegisterEngineRoutes wires FEAT-011's endpoint onto r.
func RegisterEngineRoutes(r chi.Router, workspaces WorkspaceStore, provider EngineStatusProvider, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}", handleGetWorkspace(workspaces, provider, logger))
}

// handleGetWorkspace serves screens/04-workspace-home.md's GET /api/workspaces/:id: the workspace's
// metadata merged with its LIVE engine status.
//
// This deliberately lives in engine.go rather than workspaces.go, even though "fetch one workspace by
// id" reads like a plain CRUD concern that would otherwise belong there: the entire reason this
// single-workspace GET exists in this wave is FEAT-011's honest-status requirement. The persisted
// `engine_status` column (kept in sync separately, best-effort, by
// postgres.WorkspaceRepository.EngineStatusHook via engine.WithStatusChangeHook — see that hook's
// doc comment) can lag behind reality by however long a background transition takes to be written.
// This handler instead asks engine.Provider directly, so the status pill on Workspace Home is never
// a stale read — Provider.Status's own contract guarantees that asking never itself triggers a wake,
// so this is safe to poll on every screen visit.
func handleGetWorkspace(workspaces WorkspaceStore, provider EngineStatusProvider, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		ws, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID)
		if !ok {
			return
		}

		out := toPublicWorkspace(ws)

		// A failure to read live status is NOT the same failure class as a failure to read the
		// workspace itself — the record loaded fine, only the extra honesty layer on top of it
		// didn't. Falling back to the persisted value (which is still a real, if possibly slightly
		// stale, engine_status — never a fabricated one) beats failing the whole request over what
		// is, from the user's point of view, "the page still loaded, the pill was just a beat
		// behind."
		if status, err := provider.Status(r.Context(), workspaceID); err == nil {
			out.EngineStatus = string(status)
		} else {
			logger.Warn("reading live engine status; falling back to persisted value",
				slog.String("workspace_id", workspaceID),
				slog.String("error", err.Error()),
			)
		}

		respondJSON(w, http.StatusOK, map[string]any{"workspace": out})
	}
}
