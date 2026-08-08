package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/suggestions"
)

// SuggestionSource is the persistence seam GET /workspaces/:id/suggestions depends on — the same
// four listers suggestions.Generate reads, satisfied by the postgres repositories. Defined here
// (consumer-side, matching the codebase's narrow-seam pattern) so the handler's tests use a small
// fake.
type SuggestionSource interface {
	Datasets(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
	Columns(ctx context.Context, workspaceID string) ([]domain.Column, error)
	Relationships(ctx context.Context, workspaceID string) ([]domain.Relationship, error)
	Metrics(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error)
}

// RegisterSuggestionRoutes wires FEAT-050's endpoint onto r: the deterministic question-suggestions
// read for the Ask panel's strip (screens/04-workspace-home.md, docs commit ef8f3bd).
func RegisterSuggestionRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/suggestions", handleListSuggestions(workspaces, members, source, logger))
}

// handleListSuggestions serves GET /v1/workspaces/:id/suggestions — the deterministic, metadata-only
// suggestions strip (FEAT-050). It is a pure Postgres read + template expansion: no engine query, no
// model call, no egress entry. Access is the ordinary read boundary (owner OR read-only member); the
// web UI hides the strip for read-only members, but suggestions are harmless metadata reads like any
// other workspace read.
func handleListSuggestions(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		suggested, err := suggestions.Generate(r.Context(), workspaceID, source)
		if err != nil {
			logger.Error("generating question suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"suggestions": suggested})
	}
}
