package run

import (
	"context"
	"strings"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// TestReconcileExclusionWarnings exercises FEAT-029's forward-looking share warning, computed only
// from measured rowsIn/rowsOut, gated behind config.ReconcileExclusionShareThreshold, and never
// produced for a query that groups or aggregates (where rows_out is post-group residue, not a
// genuine post-filter count).
func TestReconcileExclusionWarnings(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		rowsIn    int64
		rowsOut   int64
		nullRates map[string]float64
		wantShare bool
		wantText  string
	}{
		{
			name:   "filter that excluded over the threshold warns",
			sql:    `SELECT region, cnt FROM sales WHERE region IS NOT NULL`,
			rowsIn: 100, rowsOut: 60,
			wantShare: true, wantText: "excluded 40% of rows",
		},
		{
			name:   "share exactly at threshold warns",
			sql:    `SELECT region, cnt FROM sales WHERE region IS NOT NULL`,
			rowsIn: 100, rowsOut: 68,
			wantShare: true, wantText: "excluded 32% of rows",
		},
		{
			// 5/100 = 5% < 31% -> filter is quiet.
			name:   "share below threshold stays silent",
			sql:    `SELECT region, cnt FROM sales WHERE region IS NOT NULL`,
			rowsIn: 100, rowsOut: 95,
			wantShare: false,
		},
		{
			// GROUP BY makes rows_out post-group residue; no share is real.
			name:   "grouping never produces a filter share",
			sql:    `SELECT region, count(*) FROM sales GROUP BY region`,
			rowsIn: 1000, rowsOut: 4,
			wantShare: false,
		},
		{
			name:   "distinct never produces a filter share",
			sql:    `SELECT DISTINCT region FROM sales`,
			rowsIn: 1000, rowsOut: 4,
			wantShare: false,
		},
		{
			// An aggregate function presence likewise voids the share even without GROUP BY.
			name:   "aggregate call voids the share",
			sql:    `SELECT count(*) FROM sales`,
			rowsIn: 1000, rowsOut: 1,
			wantShare: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := reconcileExclusionWarnings(tt.sql, tt.rowsIn, tt.rowsOut, tt.nullRates)

			var hasFilterShare bool
			var filterMsg string
			for _, w := range warnings {
				if strings.Contains(w, "filtering excluded") {
					hasFilterShare = true
					filterMsg = w
					break
				}
			}
			if tt.wantShare {
				if !hasFilterShare {
					t.Fatalf("warnings = %q, want a filtering share warning", warnings)
				}
				if !strings.Contains(filterMsg, tt.wantText) {
					t.Errorf("warning = %q, want text %q", filterMsg, tt.wantText)
				}
			} else if hasFilterShare {
				t.Errorf("warnings = %+v, want no filter share for an unmeasured/grouping case", warnings)
			}
		})
	}
}

// TestReconcileExclusionWarningsNullDrop promotes the static null-dropping finding into a
// reconciliation warning for a WHERE over a profiled high-null column.
func TestReconcileExclusionWarningsNullDrop(t *testing.T) {
	warnings := reconcileExclusionWarnings(
		`SELECT * FROM customers WHERE email = 'a@example.com'`,
		100, 90,
		map[string]float64{"email": 0.3},
	)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "email") && strings.Contains(w, "null guard") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %+v, want a null-dropping reconciliation warning mentioning email", warnings)
	}
}

// newReconcileTestExecutor builds an Executor like newTestExecutor but seeds one complete dataset
// with a normal column and one PII column, so provenanceIneligibleColumns has real metadata to read
// (FEAT-030's binding rule recomputes the same set buildFactsPayload drops).
func newReconcileTestExecutor() *Executor {
	runs := &fakeRunStore{}
	steps := &fakeStepStore{}
	handle := &fakeHandle{rows: &fakeRows{cols: []string{"region", "cnt"}, rows: [][]any{{"North", int64(10)}}}}
	modelClient := &fakeModelClient{}
	audit := &fakeAudit{}
	reports := &fakeReportStore{}

	datasets := &fakeDatasetStore{datasets: []domain.Dataset{
		{ID: "ds-1", WorkspaceID: "ws-1", IngestStatus: domain.IngestComplete},
	}}
	columns := &fakeColumnStore{byDataset: map[string][]domain.Column{
		"ds-1": {
			{ID: "c-cnt", DatasetID: "ds-1", Name: "cnt"},
			{ID: "c-region", DatasetID: "ds-1", Name: "region"},
			{ID: "c-ssn", DatasetID: "ds-1", Name: "ssn", PIIFlag: true},
		},
	}}

	return NewExecutor(
		runs, steps, datasets, columns, &fakeObjectStore{},
		&fakeProvider{handle: handle}, audit, modelClient, reports,
		&fakeRelationshipLister{}, &fakeMetricLister{}, &fakeCorrectionLister{},
		discardLogger(),
	)
}

// TestBuildProvenanceClaims asserts the loading-bearing FEAT-030 rule: only a claim that names a
// real result column the model actually saw, at an in-range row, with a unique non-blank span is
// persisted; everything else is dropped so the panel never presents an untraceable number as
// verified.
func TestBuildProvenanceClaims(t *testing.T) {
	executor := newReconcileTestExecutor()

	out := domain.ExecuteStepOutput{
		Columns: []string{"region", "cnt"},
		Rows: [][]any{
			{"North", float64(214)},
			{"South", float64(96)},
		},
		RowCount: 2,
	}

	proposed := []domain.NarrativeClaim{
		{ClaimText: "214 customers in North", Column: "cnt", RowIndex: 0},
		{ClaimText: "96 in South", Column: "cnt", RowIndex: 1},
		// column does not exist in the result -> dropped
		{ClaimText: "mystery", Column: "nope", RowIndex: 0},
		// row index out of range -> dropped
		{ClaimText: "phantom row", Column: "cnt", RowIndex: 10},
		// blank span -> dropped
		{ClaimText: "   ", Column: "cnt", RowIndex: 0},
		// duplicate span -> second occurrence dropped
		{ClaimText: "214 customers in North", Column: "cnt", RowIndex: 1},
		// PII column values never reached the model -> dropped (mirrors buildFactsPayload)
		{ClaimText: "ssn figure", Column: "ssn", RowIndex: 0},
	}

	links, err := executor.buildProvenanceClaims(context.Background(), "ws-1", "SELECT region, cnt FROM revenue", out, proposed)
	if err != nil {
		t.Fatalf("buildProvenanceClaims: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("links = %d, want 2 (resolvable claims only): %+v", len(links), links)
	}

	// StatedValue is the ACTUAL cell value, replacing whatever the model claimed.
	var north *domain.ProvenanceLink
	for i := range links {
		if links[i].ClaimText == "214 customers in North" {
			north = &links[i]
		}
	}
	if north == nil {
		t.Fatalf("missing resolved North claim: %+v", links)
	}
	if north.StatedValue != "214" {
		t.Errorf("StatedValue = %q, want 214 (from the cell, not the claim)", north.StatedValue)
	}
	if north.SourceColumn != "cnt" {
		t.Errorf("SourceColumn = %q, want cnt", north.SourceColumn)
	}
	if len(north.SourceRowIDs) != 1 || north.SourceRowIDs[0] != "0" {
		t.Errorf("SourceRowIDs = %v, want [0]", north.SourceRowIDs)
	}
	if north.SourceQuery != "SELECT region, cnt FROM revenue" {
		t.Errorf("SourceQuery = %q, want the executed source SQL", north.SourceQuery)
	}
}

// TestBuildProvenanceClaimsDropsIneligibleColumns asserts a claim on a column whose values never
// reached the model (PII/excluded) is dropped, mirroring buildFactsPayload.
func TestFormatCellValue(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{in: nil, want: ""},
		{in: "¥1,234", want: "¥1,234"},
		{in: true, want: "true"},
		{in: false, want: "false"},
		{in: float64(42), want: "42"},
		{in: float64(3.5), want: "3.5"},
	}
	for _, tt := range tests {
		if got := formatCellValue(tt.in); got != tt.want {
			t.Errorf("formatCellValue(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
