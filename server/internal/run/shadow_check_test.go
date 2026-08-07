package run

import (
	"encoding/json"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// TestParseAggregateCalls pins the SELECT-clause extractor FEAT-031 keys its cross-checks on: AS
// aliases, bare (no-alias) calls, qualified columns, and count(*)/count(1) shapes.
func TestParseAggregateCalls(t *testing.T) {
	tests := []struct {
		name       string
		selectList string
		want       map[string]shadowExpr
	}{
		{
			name:       "sum with alias",
			selectList: "region, SUM(amount) AS total",
			want:       map[string]shadowExpr{"total": {fn: "sum", column: "amount"}},
		},
		{
			name:       "count star with alias",
			selectList: "region, COUNT(*) AS cnt",
			want:       map[string]shadowExpr{"cnt": {fn: "count", column: ""}},
		},
		{
			name:       "qualified column",
			selectList: "region, sum(o.amount) AS total",
			want:       map[string]shadowExpr{"total": {fn: "sum", column: "amount"}},
		},
		{
			name:       "avg parsed but cross-checked elsewhere",
			selectList: "region, avg(amount) AS avg_amt, count(1) AS n",
			want: map[string]shadowExpr{
				"avg_amt": {fn: "avg", column: "amount"},
				"n":       {fn: "count", column: ""},
			},
		},
		{
			name:       "no aggregate returns nothing",
			selectList: "region, amount",
			want:       map[string]shadowExpr{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAggregateCalls(tt.selectList)
			if len(got) != len(tt.want) {
				t.Fatalf("parseAggregateCalls(%q) = %+v, want %+v", tt.selectList, got, tt.want)
			}
			for alias, want := range tt.want {
				g, ok := got[alias]
				if !ok {
					t.Errorf("alias %q missing from %+v", alias, got)
					continue
				}
				if g.fn != want.fn || g.column != want.column {
					t.Errorf("alias %q = %+v, want %+v", alias, g, want)
				}
			}
		})
	}
}

// TestPrimaryAggregateTotal pins the "primary" side of a shadow cross-check: the sum of a result
// column across every row (grouped) or the single value (non-grouped), with NULL groups
// contributing zero exactly as a SQL SUM would.
func TestPrimaryAggregateTotal(t *testing.T) {
	out := domain.ExecuteStepOutput{
		Columns: []string{"region", "total"},
		Rows: [][]any{
			{"North", float64(100)},
			{"South", float64(200)},
			{"West", nil},
		},
	}
	got := primaryAggregateTotal(out, 1)
	if got == nil || *got != 300 {
		t.Fatalf("primaryAggregateTotal = %v, want 300", got)
	}

	// A non-numeric cell makes the column un-derivable → nil (the check is skipped, never guessed).
	out.Rows[1][1] = "N/A"
	if got := primaryAggregateTotal(out, 1); got != nil {
		t.Fatalf("primaryAggregateTotal with non-numeric = %v, want nil", got)
	}

	// A single-row non-grouped aggregate: the single value IS the total.
	single := domain.ExecuteStepOutput{
		Columns: []string{"total"},
		Rows:    [][]any{{float64(42)}},
	}
	if got := primaryAggregateTotal(single, 0); got == nil || *got != 42 {
		t.Fatalf("primaryAggregateTotal(single) = %v, want 42", got)
	}
}

// TestShadowValuesMatch pins the comparison tolerance: exact equality and tiny floating-point drift
// match; a real disagreement does not.
func TestShadowValuesMatch(t *testing.T) {
	if !shadowValuesMatch(300, 300) {
		t.Error("identical totals must match")
	}
	if !shadowValuesMatch(0.1+0.2, 0.3) {
		t.Error("floating-point drift within tolerance must match")
	}
	if shadowValuesMatch(300, 250) {
		t.Error("a 50-unit disagreement must not match")
	}
}

// TestShadowCheckSurfacesJoinFanOutMismatch drives the executor's shadow_check step (FEAT-031): a
// SUM computed across a two-table join is independently recomputed over the source table alone, the
// fan-out inflation is detected, and the mismatch is PERSISTED in the step output — surfaced, never
// silently discarded (AC2) — while the run itself completes (the check is advisory, like
// static_check).
func TestShadowCheckSurfacesJoinFanOutMismatch(t *testing.T) {
	executor, runs, steps, handle, modelClient, _, _ := newTestExecutor()

	// Two complete datasets; the SUM column lives in customers, the join fans out through orders.
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{
		{ID: "aaaaaaaaaaaaaaaa", WorkspaceID: "ws-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete},
		{ID: "bbbbbbbbbbbbbbbb", WorkspaceID: "ws-1", OriginalFilename: "orders.csv", IngestStatus: domain.IngestComplete},
	}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"aaaaaaaaaaaaaaaa": {{Name: "region", InferredType: domain.TypeString}, {Name: "amount", InferredType: domain.TypeNumber}},
		"bbbbbbbbbbbbbbbb": {{Name: "customer_id", InferredType: domain.TypeString}},
	}}

	// Primary result: two groups summing to 300. The independent shadow recomputes 250 over the
	// customers table alone — the join inflated the total by 50.
	handle.rows = &fakeRows{cols: []string{"region", "total"}, rows: [][]any{
		{"North", float64(100)},
		{"South", float64(200)},
	}}
	handle.sumRows = [][]any{{float64(250)}}
	handle.countRows = [][]any{{int64(100)}}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"sum per region over the join"}`,
		`{"sql":"SELECT region, SUM(amount) AS total FROM dataset_customers_csv_aaaaaaaa c JOIN dataset_orders_csv_bbbbbbbb o ON o.customer_id = c.id GROUP BY region"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"total","title":"Sales"}`,
		`{"narrative":"North and South total 300."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete — the shadow check is advisory (steps: %+v)", got, steps.all())
	}

	var shadowOut ShadowCheckStepOutput
	found := false
	for _, st := range steps.all() {
		if st.StepName != domain.StepShadowCheck || st.Status != domain.StepComplete {
			continue
		}
		if err := json.Unmarshal([]byte(st.Output), &shadowOut); err != nil {
			t.Fatalf("shadow_check output not parseable: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatalf("no completed shadow_check step in %+v", steps.all())
	}
	if len(shadowOut.Checks) != 1 {
		t.Fatalf("shadow_check checks = %+v, want exactly one", shadowOut.Checks)
	}
	c := shadowOut.Checks[0]
	if c.Column != "total" || c.Matched {
		t.Errorf("shadow check = %+v, want the total column MISMATCHED", c)
	}
	if c.PrimaryValue != 300 || c.ShadowValue != 250 {
		t.Errorf("shadow values = %v/%v, want primary 300 / shadow 250", c.PrimaryValue, c.ShadowValue)
	}
	if c.Message == "" {
		t.Error("a mismatched shadow check must carry an explanatory message")
	}
}

// TestShadowCheckSkipsNonAggregate pins FEAT-031's "where feasible" honesty: a row-level query has
// no independent total, and the step records that explicitly rather than fabricating a check.
func TestShadowCheckSkipsNonAggregate(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}},
	}}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select rows"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete", got)
	}
	var shadowOut ShadowCheckStepOutput
	for _, st := range steps.all() {
		if st.StepName == domain.StepShadowCheck && st.Status == domain.StepComplete {
			if err := json.Unmarshal([]byte(st.Output), &shadowOut); err != nil {
				t.Fatalf("shadow_check output not parseable: %v", err)
			}
		}
	}
	if len(shadowOut.Checks) != 0 {
		t.Errorf("shadow_check checks = %+v, want none for a non-aggregate query", shadowOut.Checks)
	}
	if shadowOut.SkippedReason == "" {
		t.Error("skipped_reason must explain why no cross-check was computed")
	}
}

// TestWhereClauseOriginalPreservesCasing pins the shadow query's WHERE extraction: it keeps the
// original column casing (a quoted mixed-case identifier must not be lowercased into a broken
// query) while still finding the clause boundary case-insensitively.
func TestWhereClauseOriginalPreservesCasing(t *testing.T) {
	sql := "SELECT region, SUM(\"Amount\") AS total FROM dataset_sales_csv_abc123de WHERE \"Region\" = 'North' GROUP BY region"
	if got := whereClauseOriginal(sql); got != `"Region" = 'North'` {
		t.Errorf("whereClauseOriginal = %q, want the quoted mixed-case column preserved", got)
	}
	upper := "SELECT count(*) FROM t WHERE region = 'N' ORDER BY region"
	if got := whereClauseOriginal(upper); got != "region = 'N'" {
		t.Errorf("whereClauseOriginal(upper WHERE) = %q, want the clause cut at ORDER BY", got)
	}
}
