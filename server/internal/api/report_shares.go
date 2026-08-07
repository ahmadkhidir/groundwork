package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// ReportShareStore is the authenticated management seam RegisterReportShareRoutes depends on,
// satisfied by internal/store/postgres.ReportShareRepository. Create takes the workspace id as a
// separate parameter because schema.md's ReportShare entity carries no workspace_id — the database
// column is a storage-layer denormalization (see the repository's doc comment).
type ReportShareStore interface {
	Create(ctx context.Context, workspaceID string, share domain.ReportShare) error
	ListActiveByWorkspace(ctx context.Context, workspaceID string) ([]domain.ReportShare, error)
	Revoke(ctx context.Context, id, workspaceID string) error
}

// ShareLookupStore is the public read seam: resolving an active share from its token. Satisfied by
// the same ReportShareRepository. Deliberately narrower than ReportShareStore — the public handler
// must not be able to create or revoke anything. The workspace id rides on the composite so the
// report lookup stays scoped without ever exposing the id to the client.
type ShareLookupStore interface {
	GetActiveByToken(ctx context.Context, token string) (postgres.ReportShareWithWorkspace, error)
}

// RegisterReportShareRoutes wires FEAT-046's owner-side endpoints onto r (screens/13's "Shared
// report links" section): create a share for a report, list a workspace's active shares, and revoke
// one. Mounted under the authenticated group by the caller (see api.go).
func RegisterReportShareRoutes(r chi.Router, workspaces WorkspaceStore, reports ReportStore, shares ReportShareStore, users UserTierLookup, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/reports/{reportId}/shares", handleCreateReportShare(workspaces, reports, shares, users, logger))
	r.Get("/workspaces/{workspaceId}/report-shares", handleListReportShares(workspaces, shares, logger))
	r.Delete("/workspaces/{workspaceId}/report-shares/{shareId}", handleRevokeReportShare(workspaces, shares, logger))
}

// RegisterPublicShareRoutes wires the UNAUTHENTICATED shared-report read endpoint
// (GET /v1/shared/reports/:shareToken) onto r. This must be mounted OUTSIDE authsession.RequireAuth:
// FEAT-046's whole model is that access is scoped to possession of the token, not account identity
// (user_flow.md's Authentication Gate). The caller — api.go — registers it on the public /v1 group.
func RegisterPublicShareRoutes(r chi.Router, reports ReportStore, shares ShareLookupStore, provenance ProvenanceStore, runSteps RunStepStore, logger *slog.Logger) {
	r.Get("/shared/reports/{shareToken}", handlePublicSharedReport(reports, shares, provenance, runSteps, logger))
}

// publicReportShare is one active share link as screens/13's list renders it. The share_token is
// included so the client can build the shareable URL (routes.sharedReport(token) in web/src/lib).
type publicReportShare struct {
	ID         string `json:"id"`
	ReportID   string `json:"report_id"`
	ShareToken string `json:"share_token"`
	CreatedAt  string `json:"created_at"`
}

func toPublicReportShare(share domain.ReportShare) publicReportShare {
	return publicReportShare{
		ID:         share.ID,
		ReportID:   share.ReportID,
		ShareToken: share.ShareToken,
		CreatedAt:  share.CreatedAt.Format(time.RFC3339),
	}
}

// handleCreateReportShare serves screens/13's "Share a report" action:
// POST /api/workspaces/:id/reports/:reportId/shares. Owner-only and Pro/Team-gated
// (monetization.md FEAT-046: Free No, Pro Yes, Team Yes — authoritative tier from UserTierLookup,
// never the session cookie's claim).
func handleCreateReportShare(workspaces WorkspaceStore, reports ReportStore, shares ReportShareStore, users UserTierLookup, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		// The report must exist IN this workspace — a share for a report the owner cannot see would
		// be a fabricated grant. The store scopes by workspace, so both missing and foreign read as
		// the same 404.
		reportID := chi.URLParam(r, "reportId")
		if _, err := reports.GetByID(r.Context(), workspaceID, reportID); err != nil {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Report not found.",
				"code":  "not_found",
			})
			return
		}

		if users != nil {
			user, _, err := users.FindByID(r.Context(), claims.UserID)
			if err != nil {
				logger.Error("looking up owner for share tier gate", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			if user.Tier != domain.TierPro && user.Tier != domain.TierTeam {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error": "Shared report links are available on the Pro and Team plans. Upgrade to share this report.",
					"code":  "upgrade_required",
				})
				return
			}
		}

		// The token is the only thing between a URL and the report's contents, so it is drawn from a
		// cryptographically secure source — 32 random bytes, hex-encoded (64 chars, URL-safe).
		token, err := newShareToken()
		if err != nil {
			logger.Error("generating share token", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		share := domain.ReportShare{
			ID:         uuid.NewString(),
			ReportID:   reportID,
			ShareToken: token,
			Revoked:    false,
			CreatedAt:  time.Now().UTC(),
		}
		if err := shares.Create(r.Context(), workspaceID, share); err != nil {
			logger.Error("creating report share", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusCreated, map[string]any{"share": toPublicReportShare(share)})
	}
}

// handleListReportShares serves screens/13's "Shared report links" list:
// GET /api/workspaces/:id/report-shares. Owner-only (sharing management is hidden for non-owners,
// per screens/13) and returns only ACTIVE (non-revoked) links — a revoked link no longer exists for
// the purposes of this list.
func handleListReportShares(workspaces WorkspaceStore, shares ReportShareStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		list, err := shares.ListActiveByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing report shares", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicReportShare, 0, len(list))
		for _, share := range list {
			out = append(out, toPublicReportShare(share))
		}
		respondJSON(w, http.StatusOK, map[string]any{"shares": out})
	}
}

// handleRevokeReportShare serves screens/13's revoke action:
// DELETE /api/workspaces/:id/report-shares/:shareId. Owner-only. Revoking flips the share's revoked
// flag, after which GetActiveByToken fails — the link stops working on the viewer's next load
// (screens/13's documented edge case: no forced disconnect of an already-open page).
func handleRevokeReportShare(workspaces WorkspaceStore, shares ReportShareStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		shareID := chi.URLParam(r, "shareId")
		if err := shares.Revoke(r.Context(), shareID, workspaceID); err != nil {
			if errors.Is(err, postgres.ErrReportShareNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Share not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("revoking report share", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"revoked": true, "share_id": shareID})
	}
}

// sharedReportPayload is the PUBLIC report shape served to a token holder: everything screens/09's
// read-only state renders — title, narrative, chart spec, expanded provenance links, reconciliation
// summary, created_at — and NOTHING that would leak the workspace: no workspace_id, no run_id, no
// parent_report_id (whose link would point at a workspace-internal route). The narrative and chart
// are the share's entire point; the internal ids are not.
type sharedReportPayload struct {
	ID                    string                       `json:"id"`
	Title                 string                       `json:"title"`
	Narrative             string                       `json:"narrative"`
	ChartSpec             domain.ChartSpec             `json:"chart_spec"`
	ProvenanceLinks       []domain.ProvenanceLink      `json:"provenance_links"`
	ReconciliationSummary domain.ReconciliationSummary `json:"reconciliation_summary"`
	CreatedAt             time.Time                    `json:"created_at"`
}

// sharedReportResponse is the public read endpoint's payload: the shareable report plus the execute
// facts the chart draws from. execute_result is null when the run's execute output is gone — the
// shared view then renders narrative + reconciliation without the chart, honestly, never an error.
type sharedReportResponse struct {
	Report        sharedReportPayload       `json:"report"`
	ExecuteResult *domain.ExecuteStepOutput `json:"execute_result"`
}

// handlePublicSharedReport serves GET /v1/shared/reports/:shareToken — FEAT-046's unauthenticated
// read-only report view. Access is by token possession; the token is resolved to an ACTIVE share,
// and a revoked or unknown token reads as a plain 404 (no account, no workspace, no enumeration).
//
// The report payload is filtered through sharedReportPayload (no workspace_id/run_id/parent link),
// and provenance links are expanded exactly as the authenticated report endpoint expands them — a
// shared report's provenance is part of the share, per FEAT-046 AC2 ("recipients can view the
// report, its charts, and its provenance").
func handlePublicSharedReport(reports ReportStore, shares ShareLookupStore, provenance ProvenanceStore, runSteps RunStepStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "shareToken")
		resolved, err := shares.GetActiveByToken(r.Context(), token)
		if err != nil {
			if errors.Is(err, postgres.ErrReportShareNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "This shared report link is no longer available or has been revoked.",
					"code":  "share_not_found",
				})
				return
			}
			logger.Error("resolving share token", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		share := resolved.Share

		// The share row denormalizes workspace_id, which is how the report lookup stays scoped
		// without ever exposing the id to the client.
		report, err := reports.GetByID(r.Context(), resolved.WorkspaceID, share.ReportID)
		if err != nil {
			// The report is gone (workspace deleted, report pruned) while the share row survives
			// (report_shares cascades on report delete, so this is genuinely a gone-workspace edge);
			// either way the holder gets the same honest not-found.
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "This shared report is no longer available.",
				"code":  "share_not_found",
			})
			return
		}

		if report.ReconciliationSummary.ExclusionWarnings == nil {
			report.ReconciliationSummary.ExclusionWarnings = []string{}
		}

		links := []domain.ProvenanceLink{}
		if provenance != nil && len(report.ProvenanceLinks) > 0 {
			if expanded, err := provenance.ListByReport(r.Context(), report.ID); err == nil {
				links = expanded
			} else {
				logger.Warn("expanding provenance links for shared report",
					slog.String("report_id", report.ID), slog.String("error", err.Error()))
			}
		}
		if report.ProvenanceLinks == nil {
			report.ProvenanceLinks = []string{}
		}

		executeResult := loadExecuteResult(r.Context(), runSteps, report.RunID)

		respondJSON(w, http.StatusOK, sharedReportResponse{
			Report: sharedReportPayload{
				ID:                    report.ID,
				Title:                 report.Title,
				Narrative:             report.Narrative,
				ChartSpec:             report.ChartSpec,
				ProvenanceLinks:       links,
				ReconciliationSummary: report.ReconciliationSummary,
				CreatedAt:             report.CreatedAt,
			},
			ExecuteResult: executeResult,
		})
	}
}

// loadExecuteResult recovers the run's executed facts for the chart. It is honest about every
// failure mode (no steps, no execute step, unparseable output) by returning nil — the shared view
// renders the narrative without a chart rather than erroring the whole report.
func loadExecuteResult(ctx context.Context, runSteps RunStepStore, runID string) *domain.ExecuteStepOutput {
	if runSteps == nil || runID == "" {
		return nil
	}
	steps, err := runSteps.ListByRun(ctx, runID)
	if err != nil {
		return nil
	}
	for _, st := range steps {
		if st.StepName != domain.StepExecute || st.Status != domain.StepComplete {
			continue
		}
		var out domain.ExecuteStepOutput
		if err := json.Unmarshal([]byte(st.Output), &out); err != nil {
			return nil
		}
		if out.Rows == nil {
			out.Rows = [][]any{}
		}
		return &out
	}
	return nil
}

// newShareToken draws 32 bytes from crypto/rand and hex-encodes them — 64 URL-safe characters, the
// unguessable secret at the heart of FEAT-046's token-possession access model.
func newShareToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
