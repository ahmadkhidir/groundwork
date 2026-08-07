package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
//
// ListByWorkspace backs FEAT-034's recent-reports list (screens/04-workspace-home.md): the
// workspace home's "recent reports" section orders a workspace's reports most-recent-first (by
// created_at), capped at limit. The store scopes by workspace in SQL, so the list never leaks a
// report from a workspace the caller does not own.
type ReportStore interface {
	GetByID(ctx context.Context, workspaceID, id string) (domain.Report, error)
	GetByRunID(ctx context.Context, workspaceID, runID string) (domain.Report, error)
	ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.Report, error)
}

// ProvenanceStore is the read seam the provenance endpoints depend on, satisfied by
// internal/store/postgres.ProvenanceRepository — the same instance the executor writes links
// through (run.WithProvenance).
type ProvenanceStore interface {
	ListByReport(ctx context.Context, reportID string) ([]domain.ProvenanceLink, error)
	GetByID(ctx context.Context, workspaceID, id string) (domain.ProvenanceLink, error)
}

// ReportDetails is ReportStore's read seam, used only to expand a report's run for the provenance
// panel (screens/10 needs the source rows, which are reconstructed from the run's persisted execute
// step). Satisfied by postgres.RunRepository.
//
// GetByID is workspace-unaware here only in the sense that the provenance handler already proved
// ownership of the report (and thus its run) before calling; the run is scoped by that same report.
type ReportRunStore interface {
	GetByID(ctx context.Context, id string) (domain.Run, error)
}

// RegisterReportRoutes wires FEAT-014's report read endpoint, FEAT-034's recent-reports list
// endpoint, and FEAT-030's provenance endpoints onto r. Mounted under the authenticated group by
// the caller (see api.go).
//
// members backs the FEAT-045 access check: every route here is a READ (report view, list,
// provenance detail), so each uses requireWorkspaceAccess (owner OR read-only member) — read-only
// members must be able to view reports and provenance per FEAT-045 AC2.
//
// provenance and runSteps may be nil: without them the report endpoint still serves the report with
// provenance_links as the raw id array (no expansion), and the provenance-panel detail endpoint is
// simply not registered.
func RegisterReportRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, reports ReportStore, provenance ProvenanceStore, runSteps RunStepStore, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/reports", handleListReports(workspaces, members, reports, logger))
	r.Get("/workspaces/{workspaceId}/reports/{reportId}", handleGetReport(workspaces, members, reports, provenance, logger))
	if provenance != nil && runSteps != nil {
		r.Get("/workspaces/{workspaceId}/reports/{reportId}/provenance/{claimId}", handleGetProvenanceClaim(workspaces, members, reports, provenance, runSteps, logger))
	}
}

// List-limit bounds for handleListReports. FEAT-034's recent-reports section is capped at 10 by the
// screen spec ("reports?limit=10"); the max clamp stops a pathological limit from pulling the whole
// table into one Workspace Home render.
const (
	reportsDefaultLimit = 10
	reportsMaxLimit     = 100
)

// publicReport is the recent-reports list endpoint's wire shape: everything the Workspace Home
// report card needs to render (title, timestamp) and link to Report View (id, run id), without the
// report's heavy self-contained payload (narrative, chart spec, provenance, reconciliation) the
// read endpoint serves on demand. Mirrors workspaces.go's publicWorkspace slim-view convention.
type publicReport struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
}

func toPublicReport(report domain.Report) publicReport {
	return publicReport{
		ID:          report.ID,
		WorkspaceID: report.WorkspaceID,
		RunID:       report.RunID,
		Title:       report.Title,
		CreatedAt:   report.CreatedAt.Format(time.RFC3339),
	}
}

// reportsLimit reads the ?limit= query parameter for the recent-reports list, defaulting to 10
// (screens/04-workspace-home.md's "reports?limit=10") and clamping to reportsMaxLimit so the value
// is always a bounded positive integer regardless of what the client sends.
func reportsLimit(r *http.Request) int {
	limit := reportsDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > reportsMaxLimit {
		limit = reportsMaxLimit
	}
	return limit
}

// handleListReports serves screens/04-workspace-home.md's Recent Reports panel:
// GET /api/workspaces/:id/reports?limit=10.
//
// Returns the workspace's most recent reports (created_at DESC) as a slim list — each entry carries
// just enough for the card to render its title, timestamp, and Report View link. Full report
// payloads remain the read endpoint's job, so this list stays cheap even for a workspace with many
// reports. Ownership is enforced here (requireWorkspaceOwnership) and the store scopes by workspace
// again in SQL, the same non-enumeration stance as every other read endpoint.
func handleListReports(workspaces WorkspaceStore, members MemberLookupStore, reports ReportStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		list, err := reports.ListByWorkspace(r.Context(), workspaceID, reportsLimit(r))
		if err != nil {
			logger.Error("listing reports", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicReport, 0, len(list))
		for _, report := range list {
			out = append(out, toPublicReport(report))
		}
		respondJSON(w, http.StatusOK, map[string]any{"reports": out})
	}
}

// handleGetReport serves screens/09-report-view.md: GET /api/workspaces/:id/reports/:reportId.
//
// The report is fully self-contained per architecture.md's Report Store — the viewer needs neither
// the session engine nor live run state to render it (narrative, chart spec, reconciliation
// summary, provenance links, parent link, timestamp all ride on the object itself).
//
// When a provenance seam is wired, the report's provenance_links (stored as ids) are expanded into
// full claim objects so the viewer can render each inline Provenance-Linked Number (screens/09) —
// the shared component needs claim_text/claim stated_value, not opaque ids.
func handleGetReport(workspaces WorkspaceStore, members MemberLookupStore, reports ReportStore, provenance ProvenanceStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
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

		// Schema.md documents every array on the Report as a non-null `string[]`. A nil Go slice
		// would marshal to JSON `null` and break clients that trust the type, so normalize to the
		// documented shape before it leaves the boundary (see run.go building these as nil).
		if report.ReconciliationSummary.ExclusionWarnings == nil {
			report.ReconciliationSummary.ExclusionWarnings = []string{}
		}

		links := []domain.ProvenanceLink{}
		if provenance != nil && len(report.ProvenanceLinks) > 0 {
			links, err = provenance.ListByReport(r.Context(), report.ID)
			if err != nil {
				// Link expansion is a presentation nicety, not the report's authority; a read failure
				// serves the report with its id array rather than erroring the whole page.
				logger.Warn("expanding provenance links", slog.String("report_id", report.ID), slog.String("error", err.Error()))
				links = []domain.ProvenanceLink{}
			}
		}
		if report.ProvenanceLinks == nil {
			report.ProvenanceLinks = []string{}
		}

		// Add the run id to each link so the webpanel's "View full run" can route to the Run Viewer
		// (screens/10) without a second fetch.
		respondJSON(w, http.StatusOK, map[string]any{
			"report": reportPayload{
				ID:                    report.ID,
				WorkspaceID:           report.WorkspaceID,
				RunID:                 report.RunID,
				Title:                 report.Title,
				Narrative:             report.Narrative,
				ChartSpec:             report.ChartSpec,
				ProvenanceLinks:       links,
				ReconciliationSummary: report.ReconciliationSummary,
				ParentReportID:        report.ParentReportID,
				CreatedAt:             report.CreatedAt,
			},
		})
	}
}

// reportPayload is the report read endpoint's wire shape: the report's fields plus claim-level
// provenance expanded from opaque ids into link objects, and the originating run id surfaced for the
// provenance panel's "View full run" navigation.
type reportPayload struct {
	ID                    string                       `json:"id"`
	WorkspaceID           string                       `json:"workspace_id"`
	RunID                 string                       `json:"run_id"`
	Title                 string                       `json:"title"`
	Narrative             string                       `json:"narrative"`
	ChartSpec             domain.ChartSpec             `json:"chart_spec"`
	ProvenanceLinks       []domain.ProvenanceLink      `json:"provenance_links"`
	ReconciliationSummary domain.ReconciliationSummary `json:"reconciliation_summary"`
	ParentReportID        string                       `json:"parent_report_id,omitempty"`
	CreatedAt             time.Time                    `json:"created_at"`
}

// provenanceDetail is the provenance-panel detail endpoint's response (screens/10):
//
//	claim:  the link (claim_text, stated_value, source_query, report id) plus run_id for "View full run"
//	source: the backing result rows/cells, with available=false when the run's execute output no
//	        longer holds the rows (an honest "This source data is no longer available" state, screens/10
//	        — never a whole-page error, and never a fabricated row set).
type provenanceDetail struct {
	Claim  provenanceClaimPayload `json:"claim"`
	Source provenanceSource       `json:"source"`
}

// provenanceClaimWire flattens claim + run_id.
type provenanceClaimPayload struct {
	ID           string   `json:"id"`
	ReportID     string   `json:"report_id"`
	RunID        string   `json:"run_id"`
	ClaimText    string   `json:"claim_text"`
	StatedValue  string   `json:"stated_value"`
	SourceQuery  string   `json:"source_query"`
	SourceColumn string   `json:"source_column"`
	SourceRowIDs []string `json:"source_row_ids"`
}

// provenanceSource is the backing rows/cells for one claim. available=false means the execute output
// is gone or does not contain the claimed rows — the honest "this source is no longer available"
// state.
type provenanceSource struct {
	Available       bool       `json:"available"`
	Columns         []string   `json:"columns,omitempty"`
	Rows            [][]string `json:"rows,omitempty"`
	HighlightColumn string     `json:"highlight_column,omitempty"`
}

// handleGetProvenanceClaim serves screens/10-provenance-panel.md:
// GET /api/workspaces/:id/reports/:reportId/provenance/:claimId.
//
// It returns the claim's stated figure and source query plus the specific backing result rows/cells
// (with the relevant column named for highlighting). The rows are reconstructed from the run's
// persisted execute step output, so "View Full Run" and claim rendering need no live engine — and if
// that output is gone, the distinct "source unavailable" state is returned rather than an error.
func handleGetProvenanceClaim(workspaces WorkspaceStore, members MemberLookupStore, reports ReportStore, provenance ProvenanceStore, runSteps RunStepStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		reportID := chi.URLParam(r, "reportId")
		report, err := reports.GetByID(r.Context(), workspaceID, reportID)
		if err != nil {
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "Report not found.", "code": "not_found"})
			return
		}

		claimID := chi.URLParam(r, "claimId")
		link, err := provenance.GetByID(r.Context(), workspaceID, claimID)
		if err != nil {
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "Claim not found.", "code": "not_found"})
			return
		}
		if link.ReportID != report.ID {
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "Claim not found.", "code": "not_found"})
			return
		}

		resp := provenanceDetail{
			Claim: provenanceClaimPayload{
				ID:           link.ID,
				ReportID:     link.ReportID,
				ClaimText:    link.ClaimText,
				StatedValue:  link.StatedValue,
				SourceQuery:  link.SourceQuery,
				SourceColumn: link.SourceColumn,
				SourceRowIDs: link.SourceRowIDs,
			},
		}
		if link.SourceRowIDs == nil {
			resp.Claim.SourceRowIDs = []string{}
		}

		// Load the run's persisted execute step and slice out the claimed rows, so the panel shows
		// the exact cells that backed the number. If the execute output is gone or the rows no longer
		// exist, source is honestly unavailable — not an error, and never fabricated.
		runID := report.RunID
		resp.Claim.RunID = runID
		populateProvenanceSource(r, &resp, runSteps, runID, link)

		respondJSON(w, http.StatusOK, map[string]any{"detail": resp})
	}
}

// populateProvenanceSource fills detail.Source from the run's execute step output. It is honest
// about the two fail cases: the run's execute step is not found (available=false), or the claimed
// row is out of range (available=false). It never fabricates a row.
func populateProvenanceSource(r *http.Request, detail *provenanceDetail, runSteps RunStepStore, runID string, link domain.ProvenanceLink) {
	steps, err := runSteps.ListByRun(r.Context(), runID)
	if err != nil {
		detail.Source.Available = false
		return
	}

	var executeOutput string
	for _, st := range steps {
		if st.StepName == domain.StepExecute {
			executeOutput = st.Output
			break
		}
	}
	if executeOutput == "" {
		detail.Source.Available = false
		return
	}

	var exec domain.ExecuteStepOutput
	if err := json.Unmarshal([]byte(executeOutput), &exec); err != nil {
		detail.Source.Available = false
		return
	}

	colIdx := -1
	for i, name := range exec.Columns {
		if strings.EqualFold(name, link.SourceColumn) {
			colIdx = i
			break
		}
	}
	if colIdx < 0 {
		detail.Source.Available = false
		return
	}

	detail.Source.Columns = exec.Columns
	detail.Source.HighlightColumn = link.SourceColumn
	for _, id := range link.SourceRowIDs {
		idx, err := strconv.Atoi(id)
		if err != nil || idx < 0 || idx >= len(exec.Rows) {
			detail.Source.Available = false
			return
		}
		row := make([]string, 0, len(exec.Columns))
		for _, v := range exec.Rows[idx] {
			row = append(row, formatCell(v))
		}
		detail.Source.Rows = append(detail.Source.Rows, row)
	}
	detail.Source.Available = true
}

// formatCell renders an executed cell value as display text for the provenance panel, preserving
// string formatting (currency, percent) and rendering nil as a readable "null" so a null cell is
// honest rather than blank.
func formatCell(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
