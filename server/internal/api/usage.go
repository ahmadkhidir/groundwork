package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// UsageStore is the persistence seam RegisterUsageRoutes depends on, satisfied by
// internal/store/postgres.UsageRepository. It is read-only: FEAT-047 is the user-facing view over
// whatever usage_records the metering work has written.
type UsageStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.UsageRecord, error)
}

// usageRecordsLimit bounds the per-record breakdown the Usage tab renders. The screen shows "simple
// stat rows" for the current period; a bounded recent list is the honest superset without pulling
// every historical record into one settings screen.
const usageRecordsLimit = 12

// usageTotals is the aggregate the Usage tab's stat rows render: the CURRENT period's engine-seconds,
// storage bytes, and model tokens. Since billing periods are a billing-system concern this wave does
// not model, the current period is the most recent usage record's values — or zeros when no record
// exists yet (an honest "no usage recorded" rather than a fabricated number).
type usageTotals struct {
	EngineSeconds float64 `json:"engine_seconds"`
	StorageBytes  int64   `json:"storage_bytes"`
	ModelTokens   int64   `json:"model_tokens"`
}

// usageResponse is GET /v1/workspaces/:id/usage's payload: the recent per-period records plus the
// current-period totals. The tier differentiation (Free = totals only, Pro/Team = detailed breakdown,
// per monetization.md FEAT-047) is a PRESENTATION choice over this same data — the web tier renders
// per the session's tier, and the endpoint returns the full data because usage visibility itself is
// never gated (FEAT-047 AC3: "available by default to any account holder").
type usageResponse struct {
	Records []domain.UsageRecord `json:"records"`
	Totals  usageTotals          `json:"totals"`
}

// RegisterUsageRoutes wires FEAT-047's endpoint onto r (screens/13's Usage tab). Unlike the other
// settings tabs, usage is visible to read-only members too — screens/13: a read-only shared member
// "sees Usage and their own access level only" — so the handler authorizes with requireWorkspaceAccess
// (owner OR member), not requireWorkspaceOwnership.
func RegisterUsageRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, usage UsageStore, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/usage", handleGetUsage(workspaces, members, usage, logger))
}

// handleGetUsage serves screens/13-workspace-settings.md's Usage tab:
// GET /api/workspaces/:id/usage — current-period engine-seconds, storage bytes, and model tokens as
// simple stat rows, plus the recent per-period breakdown for the Pro/Team view.
func handleGetUsage(workspaces WorkspaceStore, members MemberLookupStore, usage UsageStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		// Owner OR read-only member (requireWorkspaceAccess). A read-only member must be able to see
		// the workspace's usage — it is the one settings surface screens/13 grants them.
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		records, err := usage.ListByWorkspace(r.Context(), workspaceID, usageRecordsLimit)
		if err != nil {
			logger.Error("listing usage records", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		if records == nil {
			records = []domain.UsageRecord{}
		}

		totals := usageTotals{}
		if len(records) > 0 {
			latest := records[0] // ListByWorkspace orders period_start DESC.
			totals = usageTotals{
				EngineSeconds: latest.EngineSeconds,
				StorageBytes:  latest.StorageBytes,
				ModelTokens:   latest.ModelTokens,
			}
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"usage": usageResponse{Records: records, Totals: totals},
		})
	}
}
