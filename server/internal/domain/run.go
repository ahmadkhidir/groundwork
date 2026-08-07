// Package domain (this file) types the per-step payload shapes that live inside Step.Input and
// Step.Output — both JSON-ENCODED STRINGS on domain.Step (see domain.go's doc comment on Step),
// because payload shape genuinely varies by StepName. Run and Step themselves already exist in
// domain.go; this file only adds the typed shapes for parsing/building those strings at the
// boundary: plan, generate_sql, execute, chart_spec, and narrate — the five steps this wave's
// linear executor implements.
//
// Phase 2 boundary (see IMPLEMENTATION.md "Phase 2 boundary assumptions"): plan, generate_sql,
// execute, chart_spec, and narrate run this wave. static_check, reconcile, shadow_check,
// adversarial_review, and assemble are Phase 3/5 and have no payload shape here yet.
package domain

// PlanStepOutput is the parsed shape of the "plan" step's Step.Output.
//
// Answerable is the FEAT-013 AC4 / FEAT-033 boundary point: when the model judges the question
// ambiguous or unanswerable against the workspace's actual schema/semantic layer, Answerable is
// false and Reason carries a plain-language explanation shown to the user — the run then fails
// honestly rather than the executor proceeding to generate_sql on a guess.
type PlanStepOutput struct {
	Answerable bool `json:"answerable"`
	// Reason is a plain-language explanation, populated whenever Answerable is false.
	Reason string `json:"reason,omitempty"`
	// Approach is a short plan for how generate_sql should answer the question — which tables/joins/
	// metrics it expects to use — carried forward as context for the generate_sql step. Populated only
	// when Answerable is true.
	Approach string `json:"approach,omitempty"`
}

// GenerateSQLStepOutput is the parsed shape of the "generate_sql" step's Step.Output: the SQL text
// FEAT-014 (SQL Execution & Fact Return) executes.
type GenerateSQLStepOutput struct {
	SQL string `json:"sql"`
}

// ExecuteStepOutput is the parsed shape of the "execute" step's Step.Output — the facts FEAT-014
// returns from running GenerateSQLStepOutput.SQL against the workspace's engine.
type ExecuteStepOutput struct {
	Columns []string `json:"columns"`
	// Rows is the executed result set, one []any per row, in Columns order. Each element is a JSON
	// scalar (string, float64, bool, or nil) as produced by encoding/json — DuckDB-native types are
	// converted to these at the boundary in internal/run.
	Rows [][]any `json:"rows"`
	// RowCount is len(Rows), carried explicitly so a client never has to recompute it and so a future
	// reconcile step (Phase 3) has a stable field name to diff against.
	RowCount int `json:"row_count"`
}

// ChartSpecStepOutput is the parsed shape of the "chart_spec" step's Step.Output (FEAT-016).
//
// The model emits a parsable domain.ChartSpec — a chart type plus column-to-axis encodings — never
// a chart image or freeform markup. The spec is deliberately color-free: the first-party renderer
// assigns branding chart tokens (chart-cat-1..8 / chart-seq / chart-div, branding.md) in Wave 4, and
// the generation prompt forces token-compatible encodings so that assignment is always well-defined.
type ChartSpecStepOutput struct {
	ChartSpec ChartSpec `json:"chart_spec"`
}

// NarrateStepOutput is the parsed shape of the "narrate" step's Step.Output (FEAT-015 + FEAT-030).
//
// Narrative is plain-language prose over ONLY the facts returned by execute — the model never sees
// data outside the result set, and the payload it receives is built by model.BuildFactsPayload,
// which applies the same R-016 filter to result values as BuildPayload applies to metadata.
//
// Claims (FEAT-030) is the model's structured map of every numeric statement in Narrative back to a
// specific result cell: claim_text is the exact span, Column is the result-set column whose cell
// backs the figure, RowIndex is the 0-based row within the executed result set. Claims is optional
// — a deterministic validator re-checks each against the actual executed rows before any is
// persisted, and one that names a column/row that does not exist (or a flagged column whose value
// the model never saw) is dropped. This is the load-bearing half of FEAT-030 AC3: the model cannot
// produce a link to a figure with no backing cell.
type NarrateStepOutput struct {
	Narrative string           `json:"narrative"`
	Claims    []NarrativeClaim `json:"claims,omitempty"`
}

// NarrativeClaim is one figure the model stated in the narrative, tied to the exact result cell it
// claims to come from (FEAT-030). It is UNVALIDATED at parse time — internal/run's provenance
// builder binds it back to the executed result set and only persists claims that resolve to a real
// cell in a column the model actually saw (see Executor.buildProvenanceClaims).
type NarrativeClaim struct {
	// ClaimText is the exact text span in the narrative this claim corresponds to.
	ClaimText string `json:"claim_text"`
	// Column is the result column name whose single cell backs the stated figure.
	Column string `json:"column"`
	// RowIndex is the 0-based row within the executed result set holding that cell.
	RowIndex int `json:"row_index"`
}

// StaticCheckWarning is one finding recorded by FEAT-028's static SQL analysis. It is surfaced in
// the Run Viewer before the execute results are shown, but it does NOT block the run — the
// analysis is advisory, and the acceptance criteria are specifically "surfaced to the user", not
// "prevent execution" (risk_log R-002's moat is verifiability visible, not gatekeeping).
type StaticCheckWarning struct {
	// Category is a stable short key naming the risky pattern, e.g. "fan_out_join",
	// "null_dropping_filter", "hardcoded_date_window".
	Category string `json:"category"`
	// Message is a plain-language, actionable explanation.
	Message string `json:"message"`
}

// StaticCheckStepOutput is the parsed shape of the "static_check" step's Step.Output (FEAT-028):
// the findings of a deterministic, model-free analysis of the generated SQL, produced by
// internal/run before execute. It makes no engine query and makes no model call (AC3).
type StaticCheckStepOutput struct {
	Warnings []StaticCheckWarning `json:"warnings"`
}
