package run

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// runStaticCheck produces the static_check step's output (FEAT-028) for the run's generated SQL:
// a deterministic, model-free analysis that records risky patterns WITHOUT blocking execution
// (AC2's "surfaced, not buried"; AC3's "no additional model call").
//
// The analysis is grounded in the workspace's profiled columns for the null-drop check — a filter
// on a high-null column is only flagged when the data actually proves nulls exist there — and every
// other check is a pure textual pattern over the SQL. A static check NEVER runs an engine query.
func (e *Executor) runStaticCheck(ctx context.Context, sc stepContext, generateSQLOutput string) (string, error) {
	var gen domain.GenerateSQLStepOutput
	if err := json.Unmarshal([]byte(generateSQLOutput), &gen); err != nil {
		return "", fmt.Errorf("reading generate_sql output for static check: %w", err)
	}

	nullRates, err := e.nullRateByColumn(ctx, sc.workspaceID)
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(domain.StaticCheckStepOutput{Warnings: analyzeStatic(gen.SQL, nullRates)})
	if err != nil {
		return "", fmt.Errorf("encoding static_check output: %w", err)
	}
	return string(payload), nil
}

// nullRateByColumn maps lowercased column names of a workspace's COMPLETE datasets to their profiled
// null rates. Only columns with a material null rate (>= 5%) are included — the null-drop check
// only ever flags columns the data proves are meaningfully null.
func (e *Executor) nullRateByColumn(ctx context.Context, workspaceID string) (map[string]float64, error) {
	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing datasets for static check: %w", err)
	}
	out := make(map[string]float64)
	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		cols, err := e.columns.ListByDataset(ctx, ds.ID)
		if err != nil {
			return nil, fmt.Errorf("listing columns for static check: %w", err)
		}
		for _, c := range cols {
			if c.NullRate >= 0.05 {
				out[strings.ToLower(c.Name)] = c.NullRate
			}
		}
	}
	return out, nil
}

// analyzeStatic is the pure, unit-testable core of FEAT-028: it scans SQL text for the risky
// patterns the feature names — join-grain fan-out, aggregate-over-join without dedup, filters
// silently dropping nulls, and suspicious literal date windows. nullRates maps lowercased column
// names to their profiled null rate and grounds the null-drop check; an empty map disables it.
//
// Each pattern is a documented heuristic over SQL text, not a parsed AST: honest about what it can
// and cannot see, and deliberately advisory — a warning never blocks a run.
func analyzeStatic(sql string, nullRates map[string]float64) []domain.StaticCheckWarning {
	var warnings []domain.StaticCheckWarning
	lower := strings.ToLower(sql)

	// Aggregate over a join without dedup: COUNT(x) across a join without COUNT(DISTINCT), DISTINCT,
	// or GROUP BY can inflate a total when the join fan-outs a row.
	if strings.Contains(lower, " join ") && strings.Contains(lower, "count(") &&
		!strings.Contains(lower, "count(distinct") &&
		!strings.Contains(lower, "distinct ") &&
		!strings.Contains(lower, "group by") {
		warnings = append(warnings, domain.StaticCheckWarning{
			Category: "aggregate_over_join_without_dedup",
			Message:  "This query aggregates (COUNT) over a JOIN without COUNT(DISTINCT), DISTINCT, or GROUP BY, which can over-count rows if the join fans out.",
		})
	}

	// A JOIN with no ON condition can only produce a Cartesian product.
	if strings.Contains(lower, " join ") && !strings.Contains(lower, " on ") {
		warnings = append(warnings, domain.StaticCheckWarning{
			Category: "cartesian_join",
			Message:  "A JOIN has no explicit ON condition, which can produce a Cartesian product rather than the intended relationship.",
		})
	}

	// A hard-coded literal date window in the WHERE clause silently excludes anything outside it.
	if where := whereClause(lower); where != "" && literalDateRegex.MatchString(where) {
		warnings = append(warnings, domain.StaticCheckWarning{
			Category: "hardcoded_date_window",
			Message:  "A filter uses a literal date window, which silently excludes any rows outside that fixed range.",
		})
	}

	// A filter comparing a known-high-null column without guarding nulls silently drops those rows.
	if len(nullRates) > 0 {
		where := whereClause(lower)
		for col, rate := range nullRates {
			if !strings.Contains(where, col) {
				continue
			}
			if !filtersColumn(where, col) {
				continue
			}
			if strings.Contains(where, col+" is not null") || strings.Contains(where, "coalesce("+col) {
				continue
			}
			warnings = append(warnings, domain.StaticCheckWarning{
				Category: "null_dropping_filter",
				Message:  fmt.Sprintf("Column %q is %d%% null and is used in a filter without a null guard, so rows where it is null are silently dropped.", col, int(rate*100)),
			})
		}
	}

	return warnings
}

// literalDateRegex matches an ISO-ish literal date (2026-08-07 or 2026/08/07), the shape a
// hard-coded date window takes in generated SQL.
var literalDateRegex = regexp.MustCompile(`\d{4}[-/]\d{2}[-/]\d{2}`)

// whereClause returns the SQL between the first WHERE and the next major clause boundary (GROUP BY,
// ORDER BY, HAVING, LIMIT, UNION, or end), so pattern checks never reach past the filter they mean.
func whereClause(sql string) string {
	i := strings.Index(sql, " where ")
	if i < 0 {
		return ""
	}
	rest := sql[i+len(" where "):]
	for _, kw := range []string{" group by ", " order by ", " having ", " limit ", " union ", ";"} {
		if j := strings.Index(rest, kw); j >= 0 {
			rest = rest[:j]
		}
	}
	return rest
}

// filtersColumn reports whether where compares col against something — =, <>, !=, >, <, >=, <=, or
// BETWEEN. A bare reference (e.g. in IS NOT NULL) does not count as a filtering comparison.
func filtersColumn(where, col string) bool {
	comparators := []string{"=", "<>", "!=", ">", "<", ">=", "<=", "between "}
	for _, op := range comparators {
		if strings.Contains(where, col+" "+op) || strings.Contains(where, col+op) {
			return true
		}
	}
	return false
}
