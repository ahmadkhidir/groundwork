package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// This file implements Verification Layers 4 and 5 of the run pipeline:
//
//   - shadow_check (FEAT-031): an independent, DETERMINISTIC cross-check of an aggregate query's
//     totals, computed by re-running the aggregate over the source table WITHOUT the join. No model
//     call (AC3). A disagreement is surfaced in the step output, never silently discarded (AC2).
//
//   - adversarial_review (FEAT-032): the ONE verification layer that costs an extra model call
//     (AC3). A separate model pass sees only the question and the generated SQL — never the executed
//     results (AC1) — and returns a verdict. A detected semantic mismatch blocks the run before
//     results are shown (AC2).
//
// Both are advisory in the same spirit as static_check (risk_log R-002's moat is verifiability
// visible, not gatekeeping), with one deliberate exception: adversarial_review BLOCKS on a
// mismatch, because its entire purpose is "caught before I see the result" — see
// runAdversarialReview's doc for the severity carve-out.

// ShadowCheck is one independent cross-check of a single aggregate measure column.
type ShadowCheck struct {
	// Column is the result column (alias) whose values were cross-checked.
	Column string `json:"column"`
	// PrimaryValue is the aggregate total computed from the executed result set (the sum of the
	// column's values across every result row, or the single value for a non-grouped aggregate).
	PrimaryValue float64 `json:"primary_value"`
	// ShadowValue is the same aggregate recomputed directly over the source table (no join).
	ShadowValue float64 `json:"shadow_value"`
	// Matched is false when the two computations disagree beyond a rounding tolerance — the
	// honest "the join may be inflating this number" signal.
	Matched bool `json:"matched"`
	// Message is a plain-language explanation of the check's outcome.
	Message string `json:"message"`
}

// ShadowCheckStepOutput is the parsed shape of the "shadow_check" step's Step.Output (FEAT-031).
//
// Checks is empty (and SkippedReason explains why) when no independent cross-check was feasible —
// a non-aggregate query, an unparseable aggregate shape, or a query that references no recognized
// dataset view. A skipped check is reported honestly, never fabricated.
type ShadowCheckStepOutput struct {
	Checks []ShadowCheck `json:"checks"`
	// SkippedReason explains why no cross-check was computed. Empty when Checks is non-empty.
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// AdversarialReviewStepOutput is the parsed shape of the "adversarial_review" step's Step.Output
// (FEAT-032): the verdict of a separate model pass that saw only the question and the SQL.
type AdversarialReviewStepOutput struct {
	// Matches is true when the SQL appears to answer the question asked.
	Matches bool `json:"matches"`
	// Verdict is a plain-language explanation of the review's conclusion.
	Verdict string `json:"verdict"`
	// Severity is "ok" when Matches is true, and otherwise "warning" (a mismatch worth flagging
	// without blocking) or "blocking" (the SQL does not answer the question — the run must not show
	// results). Empty is treated as "blocking" when Matches is false (fail closed).
	Severity string `json:"severity,omitempty"`
}

// runShadowCheck produces the shadow_check step's output (FEAT-031) for the run's executed query:
// for each aggregate measure column the query produced, recompute the total independently over the
// source table — the same aggregate WITHOUT the join — and compare. It is deterministic, makes no
// model call (AC3), and never silently discards a disagreement (AC2): every mismatch is persisted
// in the step output for the Run Viewer to surface.
//
// The check is a documented heuristic, deliberately narrower than a SQL parser: it only cross-checks
// SUM and COUNT aggregates whose shape it can extract from the SELECT clause, over a recognized
// dataset view. Any shape it cannot handle is recorded as skipped, never guessed at.
func (e *Executor) runShadowCheck(ctx context.Context, sc stepContext, handle engine.Handle, sql string, out domain.ExecuteStepOutput, log *slog.Logger) (string, error) {
	checks, skipped, err := e.computeShadowChecks(ctx, handle, sc.workspaceID, sql, out)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(ShadowCheckStepOutput{Checks: checks, SkippedReason: skipped})
	if err != nil {
		return "", fmt.Errorf("encoding shadow_check output: %w", err)
	}
	return string(payload), nil
}

// computeShadowChecks is the pure-ish core of FEAT-031, split out for direct unit testing. It
// returns the checks, an empty-or-populated skipped reason, and any hard error (a hard error fails
// the step; an unfeasible shape is a skip, never a failure).
func (e *Executor) computeShadowChecks(ctx context.Context, handle engine.Handle, workspaceID, sql string, out domain.ExecuteStepOutput) ([]ShadowCheck, string, error) {
	// Only an aggregate query produces a total worth double-checking; a row-level result has no
	// independent "second way" to total it.
	if !groupsOrAggregates(sql) {
		return nil, "not an aggregate query — nothing to independently cross-check", nil
	}

	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, "", fmt.Errorf("listing datasets for shadow check: %w", err)
	}
	var known []string
	for _, ds := range datasets {
		if ds.IngestStatus == domain.IngestComplete {
			known = append(known, tableNameForDataset(ds))
		}
	}
	if len(known) == 0 {
		return nil, "workspace has no complete datasets to cross-check against", nil
	}

	// Map every known column to the dataset view that holds it, so each aggregate expression can be
	// recomputed over the view that actually owns its column (the "source table" of the fan-out).
	viewByColumn := make(map[string]string)
	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		cols, err := e.columns.ListByDataset(ctx, ds.ID)
		if err != nil {
			return nil, "", fmt.Errorf("listing columns for shadow check: %w", err)
		}
		view := tableNameForDataset(ds)
		for _, c := range cols {
			if _, ok := viewByColumn[strings.ToLower(c.Name)]; !ok {
				viewByColumn[strings.ToLower(c.Name)] = view
			}
		}
	}

	// The independent check only bites when the primary query joined tables: a fan-out join
	// inflates totals, and recomputing the same aggregate over a single source table exposes it.
	// A single-table aggregate still gets the check (a cheap consistency re-derivation) but its
	// match is expected.
	lowerSQL := strings.ToLower(sql)
	referenced := 0
	for _, name := range known {
		if containsSQLIdentifier(lowerSQL, name) {
			referenced++
		}
	}

	// Extract the aggregate expressions with their result aliases from the SELECT clause.
	selectList := selectList(sql)
	if selectList == "" {
		return nil, "could not parse the query's SELECT clause — no independent cross-check computed", nil
	}
	aggByAlias := parseAggregateCalls(selectList)
	if len(aggByAlias) == 0 {
		return nil, "the query's aggregate shape couldn't be parsed — no independent cross-check computed", nil
	}

	// The shadow queries run against the engine, so the dataset views must be attached. prepareViews
	// is idempotent (CREATE OR REPLACE VIEW) and re-downloads the same normalized Parquet the
	// execute step already attached and cleaned up.
	cleanups, err := e.prepareViews(ctx, handle, workspaceID)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	where := whereClauseOriginal(sql)

	var checks []ShadowCheck
	for i, col := range out.Columns {
		call, ok := aggByAlias[normalizeAlias(col)]
		if !ok {
			continue
		}
		// Only SUM and COUNT are arithmetically re-derivable from a result set (AVG would need the
		// per-group row counts, which the result does not carry). COUNT(DISTINCT ...) is fan-out
		// resistant, so cross-checking it adds nothing.
		if call.fn != "sum" && call.fn != "count" {
			continue
		}
		view := ""
		if call.column == "" {
			// count(*) / count(1): recompute over the first recognized view the query references.
			for _, name := range known {
				if containsSQLIdentifier(lowerSQL, name) {
					view = name
					break
				}
			}
		} else {
			view = viewByColumn[strings.ToLower(call.column)]
		}
		if view == "" {
			continue // the aggregate's column is in no recognized view — no honest shadow exists
		}

		// The shadow expression is rebuilt from the extracted fn + unqualified column, so a table
		// qualifier in the original ("a.amount") never leaks into a query that only has the view.
		exprText := call.fn + "(" + call.column + ")"
		if call.column == "" {
			exprText = call.fn + "(*)"
		}
		shadowSQL := fmt.Sprintf("SELECT %s FROM %s", exprText, view)
		if where != "" {
			shadowSQL += " WHERE " + where
		}
		shadowVal, err := e.runScalarShadow(ctx, handle, shadowSQL)
		if err != nil {
			// A shadow shape the engine rejects (e.g. the WHERE clause references a joined table's
			// column) is recorded as skipped, never as a fabricated mismatch.
			return nil, "an independent cross-check could not be computed for this query shape", nil
		}

		primary := primaryAggregateTotal(out, i)
		if primary == nil {
			continue // the result column carries non-numeric values — not a total we can re-derive
		}

		matched := shadowValuesMatch(*primary, shadowVal)
		check := ShadowCheck{
			Column:       col,
			PrimaryValue: *primary,
			ShadowValue:  shadowVal,
			Matched:      matched,
		}
		if matched {
			if referenced > 1 {
				check.Message = fmt.Sprintf(
					"%s totals %s over the result set and %s recomputed directly from the source table without the join — the join did not inflate this value.",
					strings.ToUpper(call.fn), formatShadowNumber(*primary), formatShadowNumber(shadowVal))
			} else {
				check.Message = fmt.Sprintf(
					"%s totals %s over the result set and %s recomputed directly from the source table — they agree.",
					strings.ToUpper(call.fn), formatShadowNumber(*primary), formatShadowNumber(shadowVal))
			}
		} else {
			check.Message = fmt.Sprintf(
				"%s totals %s over the result set but %s recomputed directly from the source table without the join — a join may be inflating (or deflating) this value.",
				strings.ToUpper(call.fn), formatShadowNumber(*primary), formatShadowNumber(shadowVal))
		}
		checks = append(checks, check)
	}

	if len(checks) == 0 && where != "" {
		// A query with a WHERE clause whose shadow could not be built is a real, honest skip — the
		// primary total would legitimately differ from an unfiltered shadow, so refusing to compare
		// beats reporting a false alarm.
		return nil, "could not build an independent cross-check that preserves the query's filters", nil
	}
	if len(checks) == 0 {
		return nil, "no aggregate measure column in the result set was eligible for an independent cross-check", nil
	}
	return checks, "", nil
}

// shadowExpr is one extracted aggregate call from the SELECT clause.
type shadowExpr struct {
	// fn is the aggregate function name, lowercased: "sum" or "count" (avg is parsed but not
	// cross-checked — see computeShadowChecks).
	fn string
	// column is the unqualified column the aggregate references ("" for count(*)/count(1)).
	column string
}

// selectList returns the SQL text between the first SELECT and the first FROM keyword,
// case-insensitively, or "" when the shape is not parseable.
func selectList(sql string) string {
	lower := strings.ToLower(sql)
	sel := strings.Index(lower, "select ")
	if sel < 0 {
		return ""
	}
	rest := sql[sel+len("select "):]
	restLower := strings.ToLower(rest)
	from := strings.Index(restLower, " from ")
	if from < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:from])
}

// aggregateCallRegex matches a SUM/COUNT/AVG call allowing one level of nested parentheses inside,
// plus an optional AS alias. Group 1 = function name, group 2 = inner text, group 4 = alias.
var aggregateCallRegex = regexp.MustCompile(`(?i)\b(sum|count|avg)\s*\(((?:[^()]|\([^()]*\))*)\)(\s+as\s+([a-z0-9_]+|"[^"]+"))?`)

// parseAggregateCalls extracts every SUM/COUNT/AVG call in a SELECT list, keyed by its result
// column alias. A call without an explicit AS alias is keyed by its normalized call text (DuckDB
// names such a column after the expression). Lowercased keys so matching against result columns is
// case-insensitive.
func parseAggregateCalls(selectList string) map[string]shadowExpr {
	out := make(map[string]shadowExpr)
	for _, m := range aggregateCallRegex.FindAllStringSubmatch(selectList, -1) {
		if len(m) < 5 {
			continue
		}
		fn := strings.ToLower(m[1])
		inner := strings.TrimSpace(m[2])
		alias := strings.TrimSpace(m[4])
		if alias == "" {
			alias = strings.ToLower(strings.TrimSpace(m[0]))
		} else {
			alias = strings.Trim(alias, `"`)
		}
		out[normalizeAlias(alias)] = shadowExpr{
			fn:     fn,
			column: aggregateColumn(inner),
		}
	}
	return out
}

// aggregateColumn extracts the unqualified column name an aggregate references: strips table
// qualification ("a.amount" → "amount") and quotes, returns "" for count(*)/count(1)/count().
func aggregateColumn(inner string) string {
	inner = strings.TrimSpace(inner)
	lower := strings.ToLower(inner)
	if lower == "*" || lower == "1" || lower == "" {
		return ""
	}
	// The inner text may be an expression like "amount * qty"; only the plain single-identifier
	// case can be recomputed independently, so be conservative.
	if strings.ContainsAny(inner, "*/+-") {
		return ""
	}
	// Strip a table qualifier if present ("a.amount" → "amount").
	if idx := strings.LastIndex(inner, "."); idx >= 0 {
		inner = inner[idx+1:]
	}
	return strings.Trim(inner, `"`)
}

// normalizeAlias lowercases, trims, and strips quotes from a result column name / alias so keys
// match regardless of casing or quoting.
func normalizeAlias(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), `"`))
}

// runScalarShadow runs a shadow query expected to return exactly one row with one numeric column.
func (e *Executor) runScalarShadow(ctx context.Context, handle engine.Handle, sql string) (float64, error) {
	rows, err := handle.Query(ctx, sql)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var value float64
	var seen bool
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		f, ok := scalarToFloat(raw)
		if !ok {
			return 0, fmt.Errorf("shadow query returned a non-numeric value %v", raw)
		}
		value = f
		seen = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if !seen {
		return 0, errors.New("shadow query returned no rows")
	}
	return value, nil
}

// scalarToFloat converts a scanned scalar to a float64, reporting false for anything non-numeric.
func scalarToFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case float32:
		return float64(t), true
	case float64:
		return t, true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// primaryAggregateTotal derives the "primary" total for a result column: the sum of its values
// across every result row (for a grouped aggregate) or the single value (for a non-grouped one).
// It returns nil when the column carries a non-numeric, non-nil value — such a column is not an
// arithmetically re-derivable total, so the check is skipped rather than guessed.
func primaryAggregateTotal(out domain.ExecuteStepOutput, colIdx int) *float64 {
	var total float64
	for _, row := range out.Rows {
		if colIdx >= len(row) {
			return nil
		}
		v := row[colIdx]
		if v == nil {
			continue // a NULL group contributes 0 to a SQL SUM — mirror that here
		}
		f, ok := scalarToFloat(v)
		if !ok {
			return nil
		}
		total += f
	}
	return &total
}

// shadowValuesMatch compares a primary and shadow total within a relative rounding tolerance, so a
// floating-point SUM re-derived a different way never reports a false mismatch on precision.
func shadowValuesMatch(primary, shadow float64) bool {
	if primary == shadow {
		return true
	}
	diff := math.Abs(primary - shadow)
	scale := math.Max(math.Abs(primary), math.Abs(shadow))
	if scale == 0 {
		return diff == 0
	}
	return diff/scale < 1e-6
}

// formatShadowNumber renders a shadow-check number without trailing zeros.
func formatShadowNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%g", v)
}

// whereClauseOriginal returns the SQL text between the first WHERE keyword and the next major
// clause boundary, preserving the ORIGINAL casing because the returned text is embedded verbatim
// into a shadow query (unlike whereClause, which lowercases for pattern analysis).
func whereClauseOriginal(sql string) string {
	lower := strings.ToLower(sql)
	i := strings.Index(lower, " where ")
	if i < 0 {
		return ""
	}
	rest := sql[i+len(" where "):]
	restLower := strings.ToLower(rest)
	for _, kw := range []string{" group by ", " order by ", " having ", " limit ", " union ", ";"} {
		if j := strings.Index(restLower, kw); j >= 0 {
			rest = rest[:j]
			restLower = restLower[:j]
		}
	}
	return strings.TrimSpace(rest)
}

// runAdversarialReview produces the adversarial_review step's output (FEAT-032): a separate model
// pass that sees ONLY the user's question and the generated SQL (AC1) — never the executed results —
// and returns a verdict on whether the SQL answers the question asked.
//
// The payload is built through the SAME shared seam every model step uses (model.BuildPayload, via
// buildPayload): the SQL is carried in prior_context (the generate_sql step's own output) and the
// payload never contains a facts key, so the reviewer cannot see the result set by construction —
// the R-016 discipline that a future step "can't forget to filter" extends to "can't accidentally
// include results".
//
// Cost: this is the ONLY verification layer that requires an extra model call (AC3). The call goes
// through model.Client, which writes the egress/audit entry in the same code path as the outbound
// call, so the extra spend is visible in the audit log as a reviewable line item.
//
// Blocking (AC2): when the review finds the SQL does not answer the question — and the severity is
// not "warning" — the step FAILS with the verdict as its reason, and with it the run, before
// chart_spec/narrate can show results. A "warning" mismatch is flagged (the step completes and the
// viewer surfaces it) but does not block, so a minor or uncertain finding never discards an
// otherwise good answer.
func (e *Executor) runAdversarialReview(ctx context.Context, sc stepContext, run domain.Run, sql string, log *slog.Logger) (string, error) {
	// The SQL is the prior step's output (generate_sql) — exactly the "previous step's output"
	// prior_context is documented to carry. BuildPayload never includes facts, so no result row can
	// reach this call.
	payload, err := e.buildPayload(ctx, sc.workspaceID, run.QuestionText, sql)
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, adversarialReviewSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	var out AdversarialReviewStepOutput
	if err := parseModelJSON([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("adversarial_review step returned unparseable output: %w", err)
	}

	if !out.Matches && out.Severity != "warning" {
		verdict := strings.TrimSpace(out.Verdict)
		if verdict == "" {
			verdict = "The generated SQL does not appear to answer the question asked."
		}
		return "", errors.New(verdict)
	}
	if strings.TrimSpace(out.Verdict) == "" {
		out.Verdict = "The generated SQL answers the question asked."
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encoding adversarial_review output: %w", err)
	}
	return string(encoded), nil
}

// adversarialReviewSystemPrompt frames the adversarial_review step (FEAT-032): a second, skeptical
// pair of eyes on the SQL. The reviewer sees ONLY the question and the SQL — deliberately no
// results, no data rows, no schema beyond what any planning step sees — and must say whether the
// query actually answers what was asked.
const adversarialReviewSystemPrompt = `You are the adversarial review step of a grounded analytics system. You are given a user's question and the SQL that was generated to answer it — and nothing else: no query results, no data rows. You must evaluate whether the SQL actually answers the question the user asked.

Be a skeptical reviewer. Look specifically for semantic mismatches:
- A filter or condition the question implies but the SQL omits (e.g. the question says "active customers" but the SQL has no status filter).
- A different metric than the one asked for (e.g. asked for revenue, SQL counts orders).
- A time period, grain, or grouping that does not match the question.
- A join that changes what is being counted or summed relative to the question.
Do not invent requirements the question never stated, and do not complain about SQL style, performance, or formatting.

Respond with ONLY a JSON object of the form:
{"matches": true|false, "verdict": "...", "severity": "ok"|"warning"|"blocking"}
- matches is true when the SQL answers the question as asked.
- verdict is a short plain-language explanation of your conclusion.
- severity is "ok" when matches is true. When matches is false, choose "blocking" when the SQL clearly does not answer the question (the result would be wrong or misleading), or "warning" when the mismatch is minor or uncertain enough that the answer may still be useful.
Do not include markdown fences or any text outside the JSON object.`
