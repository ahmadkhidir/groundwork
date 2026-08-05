package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// workspaceLimits maps a subscription tier to its FEAT-002 workspace-count ceiling, per
// monetization.md's Feature Gate Mapping row for FEAT-002 ("1 workspace" / "Up to 5" / "Unlimited").
// -1 means unlimited. Verification and privacy features are never tier-gated (rule 9) — this gate is
// purely a workspace-COUNT limit, which is exactly what monetization.md scopes tiers to.
var workspaceLimits = map[domain.SubscriptionTier]int{
	domain.TierFree: 1,
	domain.TierPro:  5,
	domain.TierTeam: -1,
}

// WorkspaceStore is the persistence seam RegisterWorkspaceRoutes depends on, satisfied by
// internal/store/postgres.WorkspaceRepository.
type WorkspaceStore interface {
	Create(ctx context.Context, ws domain.Workspace) error
	ListByOwner(ctx context.Context, ownerID string) ([]domain.Workspace, error)
	CountByOwner(ctx context.Context, ownerID string) (int, error)
	GetByID(ctx context.Context, id string) (domain.Workspace, error)
}

// UserTierLookup resolves a user's authoritative subscription tier, satisfied by
// internal/store/postgres.UserRepository (FindByID's first return value's Tier field).
//
// Re-reading this from Postgres rather than trusting the session cookie's tier claim matters: per
// web/src/auth.ts's own doc comment, that claim is "a display concern only" and "must never be the
// sole authority" for a gated action — a JWT claim is user-influenceable data once issued.
type UserTierLookup interface {
	FindByID(ctx context.Context, id string) (domain.User, string, error)
}

// RegisterWorkspaceRoutes wires FEAT-002's endpoints onto r. Every route requires an authenticated
// session (mounted under authsession.RequireAuth by the caller — see api.go).
func RegisterWorkspaceRoutes(r chi.Router, workspaces WorkspaceStore, users UserTierLookup, logger *slog.Logger) {
	r.Get("/workspaces", handleListWorkspaces(workspaces, logger))
	r.Post("/workspaces", handleCreateWorkspace(workspaces, users, logger))
}

type publicWorkspace struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	OwnerID      string `json:"owner_id"`
	EngineStatus string `json:"engine_status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func toPublicWorkspace(ws domain.Workspace) publicWorkspace {
	return publicWorkspace{
		ID:           ws.ID,
		Name:         ws.Name,
		OwnerID:      ws.OwnerID,
		EngineStatus: string(ws.EngineStatus),
		CreatedAt:    ws.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    ws.UpdatedAt.Format(time.RFC3339),
	}
}

// handleListWorkspaces serves screens/03-workspace-list.md's GET /api/workspaces: every workspace
// the caller owns.
//
// FEAT-045 (read-only sharing) is Phase 5 scope, not this wave — see workspaces.go's doc comment on
// WorkspaceStore.ListByOwner. Once it lands, this handler also merges in workspaces the caller has
// been invited into.
func handleListWorkspaces(workspaces WorkspaceStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		list, err := workspaces.ListByOwner(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("listing workspaces", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicWorkspace, 0, len(list))
		for _, ws := range list {
			out = append(out, toPublicWorkspace(ws))
		}
		respondJSON(w, http.StatusOK, map[string]any{"workspaces": out})
	}
}

type createWorkspaceRequest struct {
	Name string `json:"name"`
}

// handleCreateWorkspace serves FEAT-002's POST /api/workspaces.
//
// Per FEAT-002's acceptance criteria, creating a workspace never provisions an engine —
// WorkspaceRepository.Create writes engine_status "sleeping" and nothing here calls the Engine
// Provider seam.
func handleCreateWorkspace(workspaces WorkspaceStore, users UserTierLookup, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		var req createWorkspaceRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		name := strings.TrimSpace(req.Name)
		if name == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Workspace name is required.",
				"code":  "validation_error",
			})
			return
		}

		// The authoritative tier, read fresh from Postgres — never trust the session cookie's claim
		// for a gated action. See UserTierLookup's doc comment.
		user, _, err := users.FindByID(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("looking up user for tier gate", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		limit, ok := workspaceLimits[user.Tier]
		if !ok {
			limit = workspaceLimits[domain.TierFree]
		}
		if limit >= 0 {
			count, err := workspaces.CountByOwner(r.Context(), claims.UserID)
			if err != nil {
				logger.Error("counting workspaces for tier gate", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			if count >= limit {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error": "You've reached your plan's workspace limit. Upgrade to create another workspace.",
					"code":  "workspace_limit_reached",
				})
				return
			}
		}

		now := time.Now()
		ws := domain.Workspace{
			ID:           uuid.NewString(),
			Name:         name,
			OwnerID:      claims.UserID,
			EngineStatus: domain.EngineSleeping,
			CreatedAt:    now,
			UpdatedAt:    now,
		}

		if err := workspaces.Create(r.Context(), ws); err != nil {
			logger.Error("creating workspace", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusCreated, map[string]any{"workspace": toPublicWorkspace(ws)})
	}
}

// requireWorkspaceOwnership loads the workspace at id and confirms claims' user owns it, writing an
// appropriate error response and returning ok=false otherwise.
//
// Shared by upload.go and audit.go: architecture.md's Authentication & Authorization section is
// explicit that workspace access is scoped to ownership (or, once FEAT-045 lands, explicit sharing)
// and enforced "at the API layer, not just hidden in the UI." A 404 is returned for both "does not
// exist" and "exists but isn't yours" — the same non-enumeration principle Service.Authenticate
// already applies to login.
func requireWorkspaceOwnership(w http.ResponseWriter, r *http.Request, workspaces WorkspaceStore, userID, workspaceID string) (domain.Workspace, bool) {
	ws, err := workspaces.GetByID(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, postgres.ErrWorkspaceNotFound) {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Workspace not found.",
				"code":  "not_found",
			})
			return domain.Workspace{}, false
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return domain.Workspace{}, false
	}

	if ws.OwnerID != userID {
		respondJSON(w, http.StatusNotFound, map[string]string{
			"error": "Workspace not found.",
			"code":  "not_found",
		})
		return domain.Workspace{}, false
	}

	return ws, true
}
