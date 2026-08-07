package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ExportFormat identifies a supported report export target (screens/11-export-dialog.md).
//
// This phase implements FEAT-035 (PDF) and FEAT-036 (Excel/CSV). The remaining rows the dialog
// lists — PowerPoint, Markdown/HTML, JSON, Image — are Phase 5 features (FEAT-037..040) and are not
// registered here; see ExportDialog.tsx, which lists only the formats this phase can actually serve.
type ExportFormat string

// Export formats this phase serves.
const (
	FormatPDF ExportFormat = "pdf"
	FormatCSV ExportFormat = "csv"
)

// exportFormatTiers maps each export format to the subscription tiers allowed to request it, per
// monetization.md's Feature Gate Mapping:
//
//	FEAT-035 PDF       — Free / Pro / Team
//	FEAT-036 Excel/CSV — Pro / Team only
//
// The check is enforced in the handler against the AUTHORITATIVE tier read from Postgres via
// UserTierLookup — never the session cookie's tier claim, which is display-only data (see
// UserTierLookup's doc comment in workspaces.go).
var exportFormatTiers = map[ExportFormat]map[domain.SubscriptionTier]bool{
	FormatPDF: {
		domain.TierFree: true,
		domain.TierPro:  true,
		domain.TierTeam: true,
	},
	FormatCSV: {
		domain.TierPro:  true,
		domain.TierTeam: true,
	},
}

// ExportAuditWriter is the seam the export handler uses to record an `export` entry in the combined
// audit/egress log (FEAT-042), satisfied by internal/audit.Writer.
//
// Following the same discipline as FEAT-007's PII override and FEAT-048's deletion: the entry is
// written in the same code path as the export itself, and a failure to write it fails the request —
// an export the audit log cannot account for is exactly the silent-drift state FEAT-042 exists to
// prevent (see DeleteDatasetAndObjects' doc comment in datasets.go).
type ExportAuditWriter interface {
	WriteExport(ctx context.Context, workspaceID, userID string) error
}

// RegisterExportRoutes wires the report export endpoint onto r (screens/11-export-dialog.md,
// FEAT-035/FEAT-036). Mounted under the authenticated group by the caller (see api.go).
//
// reports, runSteps, users, and audit are all required collaborators: the handler reads the report
// and its run's persisted execute step, enforces the tier gate, and records the export. Do not
// register this with nil seams — a gate that could be skipped is not a gate.
func RegisterExportRoutes(r chi.Router, workspaces WorkspaceStore, reports ReportStore, runSteps RunStepStore, users UserTierLookup, audit ExportAuditWriter, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/reports/{reportId}/export", handleExportReport(workspaces, reports, runSteps, users, audit, logger))
}

// handleExportReport serves GET /api/workspaces/:id/reports/:reportId/export?format=pdf|csv.
//
// The endpoint is deliberately a plain authenticated GET with credentials: the browser session
// cookie travels on the request (the same way every other read endpoint authenticates), and the
// response is the export bytes with a Content-Disposition attachment header. The web client fetches
// it as a blob and triggers the download (see ExportDialog.tsx's downloadExportFile) — an anchor
// alone cannot carry the cross-origin session cookie.
//
// Pipeline, in order:
//  1. Ownership — requireWorkspaceOwnership (404 for "not yours", non-enumeration).
//  2. Format — the ?format= parameter must be a registered ExportFormat (400 otherwise).
//  3. Report — the report must exist in this workspace (404).
//  4. Tier gate — the AUTHORITATIVE tier from UserTierLookup; a format the tier cannot use is a
//     403 `upgrade_required`, so a Free user hitting the Excel/CSV row directly (screens/11's
//     "not selectable at all" edge case enforced server-side) never gets the data.
//  5. Build — CSV from the persisted execute step output (the same facts the in-app chart and data
//     table draw from, so "matches exactly what was shown" holds); PDF from the report's narrative
//     plus a vector chart drawn from the same facts (see export_pdf.go).
//  6. Audit — WriteExport in this same code path, fail-closed.
//  7. Serve — bytes with Content-Type and Content-Disposition.
//
// The session engine is never touched: everything comes from the Report Store and the run's
// persisted steps, so export works while the workspace's engine is asleep (FEAT-035 AC3).
func handleExportReport(workspaces WorkspaceStore, reports ReportStore, runSteps RunStepStore, users UserTierLookup, audit ExportAuditWriter, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		format := ExportFormat(strings.ToLower(r.URL.Query().Get("format")))
		allowedTiers, ok := exportFormatTiers[format]
		if !ok {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Unknown export format. Use format=pdf or format=csv.",
				"code":  "invalid_format",
			})
			return
		}

		reportID := chi.URLParam(r, "reportId")
		report, err := reports.GetByID(r.Context(), workspaceID, reportID)
		if err != nil {
			// The store scopes by workspace, so "does not exist" and "belongs to another workspace"
			// read as one plain 404 — the same non-enumeration stance as handleGetReport.
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Report not found.",
				"code":  "not_found",
			})
			return
		}

		// The authoritative tier, read fresh from Postgres — never the session cookie's claim for a
		// gated action. See UserTierLookup's doc comment.
		user, _, err := users.FindByID(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("looking up user for export tier gate",
				slog.String("workspace_id", workspaceID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		if !allowedTiers[user.Tier] {
			respondJSON(w, http.StatusForbidden, map[string]string{
				"error": "Excel/CSV export is available on the Pro and Team plans. Upgrade to export the underlying result set.",
				"code":  "upgrade_required",
			})
			return
		}

		// The run's persisted execute step is the single source of the underlying result set. It is
		// loaded for BOTH formats: CSV exports the facts verbatim, and the PDF draws its chart from
		// the same facts so the printed chart matches the in-app rendering.
		exec, execAvailable := loadExecuteOutput(r.Context(), runSteps, report.RunID)

		var (
			body        []byte
			contentType string
		)
		switch format {
		case FormatCSV:
			if !execAvailable {
				// The execute output IS the CSV content; without it there is nothing honest to
				// export. A 409 (rather than a 500) names the real condition: the data this report
				// summarized is no longer available, not a server fault.
				respondJSON(w, http.StatusConflict, map[string]string{
					"error": "The result data behind this report is no longer available, so it cannot be exported as Excel/CSV.",
					"code":  "result_data_unavailable",
				})
				return
			}
			body, err = buildCSV(exec)
			if err != nil {
				logger.Error("building CSV export", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			contentType = "text/csv; charset=utf-8"
		case FormatPDF:
			// The PDF degrades honestly when the execute output is gone: narrative + a note, never a
			// fabricated chart — the same "source unavailable" honesty the provenance panel applies.
			body, err = buildReportPDF(report, &exec, execAvailable)
			if err != nil {
				logger.Error("building PDF export",
					slog.String("report_id", report.ID),
					slog.String("error", err.Error()),
				)
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			contentType = "application/pdf"
		default:
			// Unreachable in practice (format was validated against exportFormatTiers above), but a
			// future format added to the gate map without a builder must fail loudly, not serve an
			// empty 200.
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "This export format is not supported yet.",
				"code":  "invalid_format",
			})
			return
		}

		// Fail-closed audit write, in this same code path: an export that happened with no audit
		// entry is the exact drift FEAT-042 exists to prevent. The file is not served unless the
		// action is recorded — the user retries and the retried export is the recorded one.
		if err := audit.WriteExport(r.Context(), workspaceID, claims.UserID); err != nil {
			logger.Error("writing export audit entry",
				slog.String("workspace_id", workspaceID),
				slog.String("report_id", report.ID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+exportFilename(report, format)+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			logger.Warn("writing export response", slog.String("error", err.Error()))
		}
	}
}

// loadExecuteOutput reads a run's persisted execute step output. The second return is false when
// the run has no readable execute step — the honest "the underlying result data is no longer
// available" condition, never fabricated rows.
func loadExecuteOutput(ctx context.Context, runSteps RunStepStore, runID string) (domain.ExecuteStepOutput, bool) {
	if runSteps == nil {
		return domain.ExecuteStepOutput{}, false
	}
	steps, err := runSteps.ListByRun(ctx, runID)
	if err != nil {
		return domain.ExecuteStepOutput{}, false
	}
	for _, step := range steps {
		if step.StepName == domain.StepExecute && step.Output != "" {
			var out domain.ExecuteStepOutput
			if err := json.Unmarshal([]byte(step.Output), &out); err == nil {
				return out, true
			}
		}
	}
	return domain.ExecuteStepOutput{}, false
}

// buildCSV renders the executed result set as RFC 4180 CSV (encoding/csv): one header row of column
// names, then one row per executed row, cells formatted exactly as the provenance panel formats
// them (formatCell in reports.go — the same display text the in-app data table draws from, so the
// export matches what was shown). Null cells export as empty, the standard CSV representation of a
// missing value.
func buildCSV(exec domain.ExecuteStepOutput) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(exec.Columns); err != nil {
		return nil, err
	}
	for _, row := range exec.Rows {
		record := make([]string, len(exec.Columns))
		for i := 0; i < len(exec.Columns) && i < len(row); i++ {
			record[i] = formatCell(row[i])
		}
		if err := w.Write(record); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// exportFilename builds the Content-Disposition filename for an export: a slug of the report title
// plus the report id prefix, so two exports from different reports never collide in a downloads
// folder and the name is readable at a glance.
func exportFilename(report domain.Report, format ExportFormat) string {
	slug := slugify(report.Title)
	if slug == "" {
		slug = "report"
	}
	id := report.ID
	if len(id) > 8 {
		id = id[:8]
	}
	switch format {
	case FormatCSV:
		return slug + "-" + id + "-results.csv"
	default:
		return slug + "-" + id + ".pdf"
	}
}

// slugify lowercases a title and keeps only [a-z0-9] (plus "-" between words) so it is safe in a
// Content-Disposition filename. Returns "" for a title with nothing slug-safe in it.
func slugify(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
