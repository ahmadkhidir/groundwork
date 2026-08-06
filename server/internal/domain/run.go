// Package domain (this file) types the per-step payload shapes that live inside Step.Input and
// Step.Output — both JSON-ENCODED STRINGS on domain.Step (see domain.go's doc comment on Step),
// because payload shape genuinely varies by StepName. Run and Step themselves already exist in
// domain.go; this file only adds the typed shapes for parsing/building those strings at the
// boundary, for the three steps this wave's linear executor implements: plan, generate_sql, execute.
//
// Phase 2 boundary (see IMPLEMENTATION.md "Phase 2 boundary assumptions"): only plan, generate_sql,
// and execute run this wave. static_check, reconcile, shadow_check, adversarial_review, chart_spec,
// narrate, and assemble are Phase 3/5 and have no payload shape here yet.
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
