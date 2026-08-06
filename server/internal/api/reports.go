package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ReportStore is the read seam the report endpoints depend on, satisfied by
// internal/store/postgres.ReportRepository.
//
// GetByID is workspace-scoped at the store: a report id that exists only in another workspace reads
// as not-found, which is the non-enumeration stance every other read endpoint takes (see
// handleGetRun's doc comment).
//
// GetByRunID backs the GET run response's report_id (FEAT-018): the run viewer uses it to auto-
// navigate to the report a completed run materialized.
type ReportStore interface {
	GetByID(ctx context.Context, workspaceID, id string) (domain.Report, error)
	GetByRunID(ctx context.Context, workspaceID, runID string) (domain.Report, error)
}

// RegisterReportRoutes wires FEAT-014's report read endpoint onto r. Mounted under the
// authenticated group by the caller (see api.go) like every other endpoint.
func RegisterReportRoutes(r chi.Router, workspaces WorkspaceStore, reports ReportStore, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/reports/{reportId}", handleGetReport(workspaces, reports, logger))
}

// handleGetReport serves screens/09-report-view.md: GET /api/workspaces/:id/reports/:reportId.
//
// The report is fully self-contained per architecture.md's Report Store — the viewer needs neither
// the session engine nor live run state to render it (narrative, chart spec, reconciliation
// summary, provenance links, parent link, timestamp all ride on the object itself).
func handleGetReport(workspaces WorkspaceStore, reports ReportStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		reportID := chi.URLParam(r, "reportId")
		report, err := reports.GetByID(r.Context(), workspaceID, reportID)
		if err != nil {
			// The store scopes by workspace, so both "does not exist" and "exists but belongs to a
			// different workspace" read as one error — a plain 404 that never confirms the existence
			// of an id the caller cannot own.
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Report not found.",
				"code":  "not_found",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"report": report})
	}
}
