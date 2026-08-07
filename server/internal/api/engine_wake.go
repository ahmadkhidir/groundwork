package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// EngineWakeProvider is the seam RegisterEngineWakeRoute depends on for triggering FEAT-012's
// wake-on-intent, satisfied by internal/engine.Provider (engine.LocalProvider this wave).
//
// It is deliberately narrower than the full Provider interface AND deliberately separate from the
// EngineStatusProvider seam RegisterEngineRoutes uses: this handler must be able to Acquire/Release
// (a wake is a provisioning action), while the status handler must never be able to (Status's
// contract forbids provoking a wake). Two seams make the distinction impossible to cross by accident.
type EngineWakeProvider interface {
	Acquire(ctx context.Context, workspaceID string) (engine.Handle, error)
	Release(handle engine.Handle) error
}

// wakeAcquireTimeout bounds how long a wake request blocks waiting for the engine to provision. It
// is a request-scope deadline, not a tunable product threshold (IMPLEMENTATION.md rule 12 scopes
// those to server-enforced product behavior; this is the same class of fixed implementation detail
// as the SSE stream's request timeout). middleware.Timeout(60s) would cut the request anyway; this
// deadline lets the handler answer with the HONEST engine_unavailable error instead of the generic
// timeout response.
const wakeAcquireTimeout = 45 * time.Second

// RegisterEngineWakeRoute wires FEAT-012's wake endpoint onto r: POST /v1/workspaces/:id/engine/wake.
// Mounted under the authenticated group by the caller (see api.go) — a wake must be attributable to
// a member of the workspace, and the Ask panel that triggers it only exists on authenticated screens.
func RegisterEngineWakeRoute(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, users UserTierLookup, provider EngineWakeProvider, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/engine/wake", handleWakeWorkspace(workspaces, members, users, provider, logger))
}

// handleWakeWorkspace serves POST /v1/workspaces/:id/engine/wake — the server half of FEAT-012's
// wake-on-intent: focusing the Ask input tells the engine to start waking so it is likely ready by
// the time the user finishes typing (screens/04's Data Flow step 2).
//
// The wake acquires the workspace's engine and immediately releases it. Acquire BLOCKS until the
// engine is ready (or the provisioning deadline passes), so a wake fired from the browser resolves
// once the engine is genuinely ready — the response carries the resulting status so the UI's status
// pill can update without waiting for its next poll. An unused wake (the user focuses then never
// asks) leaves the engine to idle out again on its own schedule (user_flow.md Flow 4 Alternate Path
// A): the only state change is the engine's own lifecycle, and the response is a success either way.
//
// The request is idempotent: waking an already-ready engine costs a fast acquire/release that re-arms
// the idle clock, and waking an already-waking engine simply joins the in-flight provisioning. This
// is what makes the client's fire-on-focus (without first checking status) safe.
func handleWakeWorkspace(workspaces WorkspaceStore, members MemberLookupStore, users UserTierLookup, provider EngineWakeProvider, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		// Waking spends metered engine-seconds on the workspace, so it goes through the gated
		// mutation check (billing milestone, pass 2): an owner whose excess workspace is read-only
		// after a downgrade must not be able to spend compute on it.
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, users, claims.UserID, workspaceID); !ok {
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), wakeAcquireTimeout)
		defer cancel()

		handle, err := provider.Acquire(ctx, workspaceID)
		if err != nil {
			if errors.Is(err, engine.ErrEngineUnavailable) {
				// Honest, retryable state: the engine could not be provisioned in time. The user's
				// question (if any) is unaffected — run creation wakes the engine on its own path.
				respondJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "The engine is taking longer than usual to wake. Try again in a moment.",
					"code":  "engine_unavailable",
				})
				return
			}
			logger.Error("waking workspace engine",
				slog.String("workspace_id", workspaceID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		defer func() { _ = provider.Release(handle) }()

		// Acquire only returns once the engine is ready, so the honest status to report is "ready".
		// The status-change hook inside the provider also mirrors waking/ready into the workspace's
		// engine_status column, so every later status read agrees with this response.
		respondJSON(w, http.StatusOK, map[string]any{"engine_status": string(engine.StatusReady)})
	}
}
