package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// reconcileExclusionWarnings computes FEAT-029's unprompted exclusion warnings for an executed query
// over the MEASURED reconciliation numbers (rows in before filtering, rows out after filtering and
// grouping) plus the workspace's profiled column null rates. It is pure — no engine query, no model
// call — so the exact same code path runs in the unit tests.
//
// Two honest kinds of warning come out of it:
//
//  1. A filter that discarded a share of rows at or above config.ReconcileExclusionShareThreshold,
//     surfaced as an unprompted callout (features.md's "excluded 31% of rows where region was null"
//     is the product's lead example). This is only computed for a query that does NOT group or
//     aggregate (groupsOrAggregates), because for an aggregate query rows_out is post-group residue,
//     not a post-filter count — comparing it to rows_in would manufacture a share that isn't real.
//
//  2. Null-dropping findings already proven by the static analysis: a WHERE filter over a column the
//     profiler proves is materially null silently drops those null rows. These are the same
//     data-grounded findings FEAT-028 surfaces in the Run Viewer, now promoted into the Report's
//     reconciliation data so the narrative can call them out too.
//
// Every share in both kinds is computed only from measured rowsIn/rowsOut or profiled null
// rates — never a fabricated figure (the "rows_in could not be measured" case is reported separately
// by measureRowsIn).
func reconcileExclusionWarnings(sql string, rowsIn, rowsOut int64, nullRates map[string]float64) []string {
	var warnings []string

	if rowsIn > 0 && !groupsOrAggregates(sql) {
		share := float64(rowsIn-rowsOut) / float64(rowsIn)
		if share >= config.ReconcileExclusionShareThreshold && share <= 1 {
			warnings = append(warnings, fmt.Sprintf(
				"This query's filtering excluded %d%% of rows (%d read, %d remained) — the reported numbers describe a subset of the source data.",
				int(share*100), rowsIn, rowsOut))
		}
	}

	// Null-dropping findings are already data-grounded messages; surface them as reconciliation
	// warnings in addition to the run-viewer advisory.
	for _, w := range analyzeStatic(sql, nullRates) {
		if w.Category == "null_dropping_filter" {
			warnings = append(warnings, w.Message)
		}
	}
	return warnings
}

// groupsOrAggregates reports whether sql groups or aggregates its rows (GROUP BY, DISTINCT, or an
// aggregate call), which makes rows_out a post-group count rather than a genuine post-filter count.
// Reconciliation only computes a filter-share warning over non-grouping queries, so it never
// reports a post-group residue as if it were a filter drop.
func groupsOrAggregates(sql string) bool {
	lower := strings.ToLower(sql)
	if strings.Contains(lower, "group by") || strings.Contains(lower, " distinct ") {
		return true
	}
	for _, fn := range []string{"count(", "sum(", "avg(", "min(", "max(", "median("} {
		if strings.Contains(lower, fn) {
			return true
		}
	}
	return false
}

// buildProvenanceClaims binds the model's proposed narrative claims to ACTUAL executed result cells,
// turning each resolved claim into a persisted ProvenanceLink (FEAT-030). This is the loading
// bearing half of AC3: a claim is persisted only if (a) it names a column that exists in the
// executed result AND whose values the model actually saw (the value of a PII/excluded
// column was never in the facts payload, so a claim on it is impossible to back), and (b) its row
// index is within the executed result set. Every resolved link keeps:
//   - ClaimText: the exact narrative span (uniquified — a model proposing the same span twice is a
//     duplicate, not two claims);
//   - StatedValue: the ACTUAL cell value, formatted, replacing whatever the model _claimed_ — the
//     panel shows the real number from the data, not a restated figure;
//   - SourceQuery: the exact SQL that produced the cell's result set;
//   - SourceColumn: the column whose cell backs the figure (for panel highlighting);
//   - SourceRowIDs: the row position within the result set.
//
// Unresolvable claims (bad column, out-of-range row, blank span, duplicate) are silently dropped:
// the narrative keeps its number, it just is not back-linked. A I claim that cannot be traced is
// exactly the failure mode AC3 forbids from being presented as verified.
func (e *Executor) buildProvenanceClaims(ctx context.Context, workspaceID, sourceQuery string, out domain.ExecuteStepOutput, proposed []domain.NarrativeClaim) ([]domain.ProvenanceLink, error) {
	colIdx := make(map[string]int, len(out.Columns))
	for i, name := range out.Columns {
		colIdx[name] = i
	}

	ineligible, err := e.provenanceIneligibleColumns(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	var links []domain.ProvenanceLink
	seen := make(map[string]bool)
	for _, nc := range proposed {
		idx, ok := colIdx[nc.Column]
		if !ok {
			continue // column not in the result set
		}
		if ineligible[strings.ToLower(strings.TrimSpace(nc.Column))] {
			continue // its values never reached the model — impossible to back
		}
		if nc.RowIndex < 0 || nc.RowIndex >= len(out.Rows) {
			continue
		}
		row := out.Rows[nc.RowIndex]
		if idx >= len(row) {
			continue
		}
		span := strings.TrimSpace(nc.ClaimText)
		if span == "" || seen[span] {
			continue
		}
		seen[span] = true
		links = append(links, domain.ProvenanceLink{
			ID:           uuid.NewString(),
			ClaimText:    span,
			StatedValue:  formatCellValue(row[idx]),
			SourceQuery:  sourceQuery,
			SourceColumn: nc.Column,
			SourceRowIDs: []string{strconv.Itoa(nc.RowIndex)},
		})
	}
	return links, nil
}

// provenanceIneligibleColumns returns the lowercased names of every result column whose VALUES never
// reach the model — any column whose PIIFlag or ExcludedFromModel is set in the workspace metadata —
// because those are the only columns a claim cannot be backed on. It mirrors the payload
// construction rule (model.buildFactsPayload drops these columns' values) by recomputing the same
// set from the workspace metadata assembled by assembleTables.
func (e *Executor) provenanceIneligibleColumns(ctx context.Context, workspaceID string) (map[string]bool, error) {
	tables, err := e.assembleTables(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, t := range tables {
		for _, c := range t.Columns {
			if c.PIIFlag || c.ExcludedFromModel {
				out[strings.ToLower(strings.TrimSpace(c.Name))] = true
			}
		}
	}
	return out, nil
}

// formatCellValue renders a single executed cell (a JSON scalar) back to its display text for the
// provenance panel's stated figure. Strings pass through verbatim (currency/percent/date formatting
// is preserved), numbers are formatted without trailing zeros, and nil renders as an empty string
// so a null cell is an honest gap rather than the words "null".
func formatCellValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
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
		return fmt.Sprintf("%v", t)
	}
}
