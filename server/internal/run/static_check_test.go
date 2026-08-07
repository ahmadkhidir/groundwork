package run

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// TestAnalyzeStatic pins FEAT-028's pure, deterministic analysis core: each risky pattern the feature
// names produces exactly the documented warning category, clean SQL produces none, and grounded
// null-guards suppress the null-drop warning.
func TestAnalyzeStatic(t *testing.T) {
	tests := []struct {
		name         string
		sql          string
		nullRates    map[string]float64
		wantCategory string // empty means expect no warnings
	}{
		{
			name: "clean aggregate with group by",
			sql:  `SELECT region, COUNT(*) AS cnt FROM dataset_sales_csv_d5f3e2a1 GROUP BY region`,
		},
		{
			name:         "aggregate over join without dedup",
			sql:          `SELECT region, COUNT(c.id) AS c FROM customers c JOIN orders o ON o.customer_id = c.id`,
			wantCategory: "aggregate_over_join_without_dedup",
		},
		{
			name:         "join without on is a cartesian product",
			sql:          `SELECT * FROM customers c JOIN orders o`,
			wantCategory: "cartesian_join",
		},
		{
			name:         "hardcoded date window",
			sql:          `SELECT amount FROM sales WHERE created_at > '2026-08-07'`,
			wantCategory: "hardcoded_date_window",
		},
		{
			name:         "filter on profiled high-null column without guard",
			sql:          `SELECT * FROM customers WHERE email = 'a@example.com'`,
			nullRates:    map[string]float64{"email": 0.3},
			wantCategory: "null_dropping_filter",
		},
		{
			name:      "null guard suppresses the null-drop warning",
			sql:       `SELECT * FROM customers WHERE email IS NOT NULL AND email = 'a@example.com'`,
			nullRates: map[string]float64{"email": 0.3},
		},
		{
			name:      "null check on a column with no profiled nulls stays silent",
			sql:       `SELECT * FROM customers WHERE region = 'North'`,
			nullRates: map[string]float64{"email": 0.3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := analyzeStatic(tt.sql, tt.nullRates)
			if tt.wantCategory == "" {
				if len(warnings) != 0 {
					t.Fatalf("warnings = %+v, want none", warnings)
				}
				return
			}
			if len(warnings) != 1 {
				t.Fatalf("warnings = %+v, want exactly one %q", warnings, tt.wantCategory)
			}
			if warnings[0].Category != tt.wantCategory {
				t.Errorf("category = %q, want %q", warnings[0].Category, tt.wantCategory)
			}
			if warnings[0].Message == "" {
				t.Errorf("warning has an empty message")
			}
		})
	}
}

// TestRunStaticCheckGroundedNullRates pins runStaticCheck's grounding: null rates come only from
// COMPLETE datasets' profiled columns (a still-ingesting dataset's schema is provisional and never
// grounds a warning), and the output marshals into the persisted StaticCheckStepOutput shape.
func TestRunStaticCheckGroundedNullRates(t *testing.T) {
	executor, _, _, _, _, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{
		{ID: "aaaaaaaaaaaaaaaa", WorkspaceID: "ws-1", IngestStatus: domain.IngestComplete},
		{ID: "bbbbbbbbbbbbbbbb", WorkspaceID: "ws-1", IngestStatus: domain.IngestPending},
	}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"aaaaaaaaaaaaaaaa": {
			{Name: "email", InferredType: domain.TypeString, NullRate: 0.3},
			{Name: "region", InferredType: domain.TypeString, NullRate: 0.0},
		},
		"bbbbbbbbbbbbbbbb": {
			{Name: "owner", InferredType: domain.TypeString, NullRate: 0.9},
		},
	}}

	sc := stepContext{runID: "run-1", workspaceID: "ws-1", userID: "user-1"}
	out, err := executor.runStaticCheck(context.Background(), sc,
		`{"sql":"SELECT * FROM dataset_a WHERE email = 'x' AND owner = 'y'"}`)
	if err != nil {
		t.Fatalf("runStaticCheck: %v", err)
	}

	var payload domain.StaticCheckStepOutput
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output not a StaticCheckStepOutput: %v", err)
	}
	if len(payload.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly the email null-drop (owner is in a pending dataset)", payload.Warnings)
	}
	if payload.Warnings[0].Category != "null_dropping_filter" {
		t.Errorf("category = %q, want null_dropping_filter", payload.Warnings[0].Category)
	}
}

// TestRunStaticCheckCleanSQL has no warnings even with a grounded workspace: clean, deterministic SQL
// produces an empty (but valid) warnings list.
func TestRunStaticCheckCleanSQL(t *testing.T) {
	executor, _, _, _, _, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "aaaaaaaaaaaaaaaa", WorkspaceID: "ws-1", IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"aaaaaaaaaaaaaaaa": {
			{Name: "region", InferredType: domain.TypeString, NullRate: 0.3},
		},
	}}

	sc := stepContext{runID: "run-1", workspaceID: "ws-1", userID: "user-1"}
	out, err := executor.runStaticCheck(context.Background(), sc,
		`{"sql":"SELECT region, COUNT(*) AS cnt FROM dataset_a GROUP BY region"}`)
	if err != nil {
		t.Fatalf("runStaticCheck: %v", err)
	}
	var payload domain.StaticCheckStepOutput
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output not a StaticCheckStepOutput: %v", err)
	}
	if len(payload.Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", payload.Warnings)
	}
}
