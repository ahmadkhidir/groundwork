package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// AuditStore is the persistence seam RegisterAuditRoutes depends on, satisfied by
// internal/store/postgres.AuditRepository.
type AuditStore interface {
	List(ctx context.Context, filter postgres.ListFilter) (postgres.Page, error)
}

// RegisterAuditRoutes wires FEAT-042's endpoint onto r.
func RegisterAuditRoutes(r chi.Router, workspaces WorkspaceStore, auditLog AuditStore, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/audit-log", handleListAuditLog(workspaces, auditLog, logger))
}

type publicAuditEntry struct {
	ID                 string `json:"id"`
	WorkspaceID        string `json:"workspace_id"`
	UserID             string `json:"user_id"`
	ActionType         string `json:"action_type"`
	PayloadSentToModel string `json:"payload_sent_to_model,omitempty"`
	Timestamp          string `json:"timestamp"`
	// Scope is a display-only convenience so the client never has to re-derive "This workspace" vs.
	// "Account" from an empty workspace_id itself — screens/17-egress-audit-log.md's Scope badge.
	Scope string `json:"scope"`
}

func toPublicAuditEntry(entry domain.AuditLogEntry) publicAuditEntry {
	scope := "workspace"
	if entry.WorkspaceID == "" {
		scope = "account"
	}
	return publicAuditEntry{
		ID:                 entry.ID,
		WorkspaceID:        entry.WorkspaceID,
		UserID:             entry.UserID,
		ActionType:         string(entry.ActionType),
		PayloadSentToModel: entry.PayloadSentToModel,
		Timestamp:          entry.Timestamp.Format(time.RFC3339),
		Scope:              scope,
	}
}

// handleListAuditLog serves FEAT-042's GET /api/workspaces/:id/audit-log.
//
// Per screens/17-egress-audit-log.md: "query_executed, model_call, export, and deletion entries are
// scoped to this workspace; login entries are account-scoped... this screen shows the current user's
// own login entries alongside them" — the merge happens server-side in
// postgres.AuditRepository.List so the client never needs a second request. Access is owner-only:
// read-only shared members (FEAT-045, Phase 5, not yet built) are explicitly excluded per that same
// screen's edge cases — "a read-only member should not see what the model was sent on the owner's
// behalf" — which requireWorkspaceOwnership already enforces by rejecting any non-owner with 404.
func handleListAuditLog(workspaces WorkspaceStore, auditLog AuditStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		filter := postgres.ListFilter{
			WorkspaceID:      workspaceID,
			RequestingUserID: claims.UserID,
			ActionType:       r.URL.Query().Get("action_type"),
			Cursor:           r.URL.Query().Get("cursor"),
		}

		if raw := r.URL.Query().Get("date_from"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "date_from must be an RFC3339 timestamp.",
					"code":  "validation_error",
				})
				return
			}
			filter.DateFrom = parsed
		}
		if raw := r.URL.Query().Get("date_to"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "date_to must be an RFC3339 timestamp.",
					"code":  "validation_error",
				})
				return
			}
			filter.DateTo = parsed
		}

		page, err := auditLog.List(r.Context(), filter)
		if err != nil {
			if errors.Is(err, postgres.ErrInvalidCursor) {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Invalid pagination cursor.",
					"code":  "validation_error",
				})
				return
			}
			logger.Error("listing audit log", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		entries := make([]publicAuditEntry, 0, len(page.Entries))
		for _, entry := range page.Entries {
			entries = append(entries, toPublicAuditEntry(entry))
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"entries":     entries,
			"next_cursor": page.NextCursor,
		})
	}
}
