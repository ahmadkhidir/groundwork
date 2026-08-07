package api

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// JSON export (FEAT-039): a stable, documented schema for programmatic consumers.
//
// Schema version 1 — the wire shape below is the contract; the envelope fields (schema_version,
// exported_at) are additive-only, and report mirrors the report read endpoint's payload so a
// consumer can trust the same shapes it already parses:
//
//	{
//	  "schema_version": 1,
//	  "exported_at": "2026-08-07T12:00:00Z",
//	  "report": {
//	    "id": string, "workspace_id": string, "run_id": string, "title": string,
//	    "narrative": string,
//	    "chart_spec": {
//	      "chart_type": string,          // bar | line | pie | scatter | table
//	      "x_axis_column": string, "y_axis_column": string,
//	      "series_column": string,      // "" when single-series
//	      "title": string
//	    },
//	    "provenance_links": [
//	      { "id": string, "report_id": string, "claim_text": string, "stated_value": string,
//	        "source_query": string, "source_column": string, "source_row_ids": string[] }
//	    ],
//	    "reconciliation_summary": {
//	      "rows_in": number, "rows_out": number, "exclusion_warnings": string[]
//	    },
//	    "parent_report_id": string,      // "" when this report has no parent
//	    "created_at": "RFC3339"
//	  },
//	  "execute_output": {                // null when the result data is no longer available
//	    "columns": string[],
//	    "rows": (string|number|boolean|null)[][],
//	    "row_count": number
//	  } | null
//	}
//
// provenance_links is always an array of full claim objects — the shape the report read endpoint
// serves (FEAT-030), which is what "including … provenance links" means for a programmatic
// consumer; it is empty when the provenance seam is unavailable or the read fails, never an error.
// execute_output carries the raw result set the chart and table draw from (not display-formatted
// text), so downstream systems can recompute or re-render the chart; it is null when the data is
// gone, the honest "result data unavailable" state the CSV export reports as a 409.
func buildReportJSON(report domain.Report, links []domain.ProvenanceLink, exec *domain.ExecuteStepOutput, execAvailable bool) ([]byte, error) {
	if report.ProvenanceLinks == nil {
		report.ProvenanceLinks = []string{}
	}
	if report.ReconciliationSummary.ExclusionWarnings == nil {
		report.ReconciliationSummary.ExclusionWarnings = []string{}
	}

	var executeOutput *executeJSON
	if execAvailable && exec != nil {
		executeOutput = &executeJSON{
			Columns:  exec.Columns,
			Rows:     exec.Rows,
			RowCount: exec.RowCount,
		}
	}

	out := reportExportJSON{
		SchemaVersion: reportJSONSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		Report: reportJSON{
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
		ExecuteOutput: executeOutput,
	}

	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding json export: %w", err)
	}
	return body, nil
}

// reportJSONSchemaVersion is the JSON export schema version. Bump it only for a BREAKING change to
// the wire shape above; additive fields keep the same version.
const reportJSONSchemaVersion = 1

// reportExportJSON is the export envelope (schema version 1, see buildReportJSON's doc comment).
type reportExportJSON struct {
	SchemaVersion int          `json:"schema_version"`
	ExportedAt    time.Time    `json:"exported_at"`
	Report        reportJSON   `json:"report"`
	ExecuteOutput *executeJSON `json:"execute_output"`
}

// reportJSON mirrors the report read endpoint's reportPayload — the documented wire shape for a
// report object (schema.md Report, with provenance_links expanded per FEAT-030).
type reportJSON struct {
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

// executeJSON is the executed result set, raw (unformatted) cell values.
type executeJSON struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	RowCount int      `json:"row_count"`
}
