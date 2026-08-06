package model

import (
	"encoding/json"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/semantic"
)

// TestBuildPayloadReducesFlaggedColumnsToNameAndType is the FEAT-013 / R-016 enforcement test: a
// column whose PIIFlag is set must reach the model as name and type only — never its profiling
// aggregates, and a fortiori never row values (which the Payload shape cannot carry at all). This
// test is what fails if a future change to BuildPayload stops filtering.
func TestBuildPayloadReducesFlaggedColumnsToNameAndType(t *testing.T) {
	payload := BuildPayload(
		"how many customers per region?",
		"",
		[]TableMetadata{{
			DatasetID: "ds-1",
			Name:      "dataset_customers_a1b2c3d4",
			Columns: []ColumnMetadata{
				{Name: "region", Type: "string", NullRate: 0.02, DistinctCount: 12},
				{Name: "email", Type: "string", PIIFlag: true, NullRate: 0.1, DistinctCount: 500},
			},
		}},
		semantic.GroundingContext{},
	)

	body, err := payload.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("decoding payload JSON: %v", err)
	}

	tables, ok := wire["tables"].([]any)
	if !ok || len(tables) != 1 {
		t.Fatalf("tables = %v, want one table", wire["tables"])
	}
	table := tables[0].(map[string]any)
	cols, ok := table["columns"].([]any)
	if !ok || len(cols) != 2 {
		t.Fatalf("columns = %v, want two", table["columns"])
	}

	region := cols[0].(map[string]any)
	if region["null_rate"] == nil || region["distinct_count"] == nil {
		t.Errorf("unflagged column lost its profiling aggregates: %v", region)
	}

	email := cols[1].(map[string]any)
	if email["name"] != "email" || email["type"] != "string" {
		t.Errorf("flagged column name/type = %v", email)
	}
	if email["null_rate"] != nil {
		t.Errorf("flagged column leaked null_rate: %v", email)
	}
	if email["distinct_count"] != nil {
		t.Errorf("flagged column leaked distinct_count: %v", email)
	}
}

// TestBuildPayloadReducesExcludedColumnsToNameAndType covers the same rule for ExcludedFromModel
// (FEAT-010's field, already part of the payload builder's contract per features.md FEAT-013).
func TestBuildPayloadReducesExcludedColumnsToNameAndType(t *testing.T) {
	payload := BuildPayload(
		"total sales",
		"",
		[]TableMetadata{{
			DatasetID: "ds-1",
			Name:      "dataset_sales_a1b2c3d4",
			Columns: []ColumnMetadata{
				{Name: "amount", Type: "number", NullRate: 0, DistinctCount: 42},
				{Name: "customer_name", Type: "string", ExcludedFromModel: true, NullRate: 0.05, DistinctCount: 7},
			},
		}},
		semantic.GroundingContext{},
	)

	body, err := payload.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("decoding payload JSON: %v", err)
	}
	cols := wire["tables"].([]any)[0].(map[string]any)["columns"].([]any)

	excluded := cols[1].(map[string]any)
	if excluded["name"] != "customer_name" || excluded["type"] != "string" {
		t.Errorf("excluded column name/type = %v", excluded)
	}
	if excluded["null_rate"] != nil || excluded["distinct_count"] != nil {
		t.Errorf("excluded column leaked aggregates: %v", excluded)
	}
}

// TestBuildPayloadWireShape pins the exact JSON keys a Payload serializes to. The model prompt and
// the egress log both consume these bytes, so a rename here would silently change both.
func TestBuildPayloadWireShape(t *testing.T) {
	payload := BuildPayload(
		"question text",
		"prior approach",
		[]TableMetadata{{
			DatasetID: "ds-1",
			Name:      "dataset_sales_a1b2c3d4",
			Columns:   []ColumnMetadata{{Name: "amount", Type: "number"}},
		}},
		semantic.GroundingContext{
			WorkspaceID: "ws-1",
			Relationships: []semantic.RelationshipGrounding{{
				SourceColumnID: "c1", TargetColumnID: "c2", Cardinality: "one_to_many",
			}},
			Metrics:     []semantic.MetricGrounding{{Name: "revenue", SQLExpression: "sum(amount)"}},
			Corrections: []semantic.CorrectionGrounding{{Description: "d", CorrectedLogic: "c"}},
		},
	)

	body, err := payload.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("decoding payload JSON: %v", err)
	}

	for _, key := range []string{"question", "prior_context", "tables"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("missing wire key %q in %s", key, body)
		}
	}
	if wire["question"] != "question text" {
		t.Errorf("question = %v", wire["question"])
	}
	if wire["prior_context"] != "prior approach" {
		t.Errorf("prior_context = %v", wire["prior_context"])
	}

	rels, ok := wire["relationships"].([]any)
	if !ok || len(rels) != 1 {
		t.Fatalf("relationships = %v, want one", wire["relationships"])
	}
	rel := rels[0].(map[string]any)
	if rel["source_column_id"] != "c1" || rel["target_column_id"] != "c2" || rel["cardinality"] != "one_to_many" {
		t.Errorf("relationship wire shape = %v", rel)
	}
}

// TestBuildPayloadEmptyTables never lets an empty table list through as nil: the wire payload must
// always carry a JSON array so the model and the egress log see the same shape.
func TestBuildPayloadEmptyTables(t *testing.T) {
	payload := BuildPayload("q", "", nil, semantic.GroundingContext{})
	body, err := payload.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("decoding payload JSON: %v", err)
	}
	if tables, ok := wire["tables"].([]any); !ok || tables == nil {
		t.Errorf("tables = %#v, want an empty JSON array, never null", wire["tables"])
	}
}
