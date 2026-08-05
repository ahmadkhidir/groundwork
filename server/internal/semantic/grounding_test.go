package semantic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

type fakeRelationshipLister struct {
	byWorkspace map[string][]domain.Relationship
	err         error
}

func (f *fakeRelationshipLister) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Relationship, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byWorkspace[workspaceID], nil
}

type fakeMetricDefinitionLister struct {
	byWorkspace map[string][]domain.MetricDefinition
	err         error
}

func (f *fakeMetricDefinitionLister) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byWorkspace[workspaceID], nil
}

type fakeCorrectionLister struct {
	byWorkspace map[string][]domain.Correction
	err         error
}

func (f *fakeCorrectionLister) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Correction, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byWorkspace[workspaceID], nil
}

// TestAssembleGroundingContextShape locks down the assembler's exact output shape — the contract
// FEAT-013 (Phase 2, next wave) is expected to build against. A change here is a deliberate contract
// change, not an incidental refactor.
func TestAssembleGroundingContextShape(t *testing.T) {
	relationships := &fakeRelationshipLister{byWorkspace: map[string][]domain.Relationship{
		"ws-1": {
			{
				ID: "rel-1", WorkspaceID: "ws-1",
				SourceColumnID: "col-a", TargetColumnID: "col-b",
				Cardinality: domain.OneToMany, GrainNotes: "one row per order line",
			},
		},
	}}
	metrics := &fakeMetricDefinitionLister{byWorkspace: map[string][]domain.MetricDefinition{
		"ws-1": {
			{
				ID: "metric-1", WorkspaceID: "ws-1",
				Name: "revenue", SQLExpression: "SUM(total) FILTER (WHERE status != 'cancelled')",
				Description: "Excludes cancelled orders.",
			},
		},
	}}
	corrections := &fakeCorrectionLister{byWorkspace: map[string][]domain.Correction{
		"ws-1": {
			{
				ID: "corr-1", WorkspaceID: "ws-1",
				Description:      "refunds join now excludes cancelled orders",
				OriginalLogic:    "JOIN refunds ON orders.id = refunds.order_id",
				CorrectedLogic:   "JOIN refunds ON orders.id = refunds.order_id WHERE orders.status != 'cancelled'",
				OriginatingRunID: "run-1",
			},
		},
	}}

	got, err := AssembleGroundingContext(context.Background(), "ws-1", relationships, metrics, corrections)
	if err != nil {
		t.Fatalf("AssembleGroundingContext: %v", err)
	}

	want := GroundingContext{
		WorkspaceID: "ws-1",
		Relationships: []RelationshipGrounding{
			{SourceColumnID: "col-a", TargetColumnID: "col-b", Cardinality: domain.OneToMany, GrainNotes: "one row per order line"},
		},
		Metrics: []MetricGrounding{
			{Name: "revenue", SQLExpression: "SUM(total) FILTER (WHERE status != 'cancelled')", Description: "Excludes cancelled orders."},
		},
		Corrections: []CorrectionGrounding{
			{Description: "refunds join now excludes cancelled orders", CorrectedLogic: "JOIN refunds ON orders.id = refunds.order_id WHERE orders.status != 'cancelled'"},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AssembleGroundingContext() = %+v, want %+v", got, want)
	}
}

// TestAssembleGroundingContextNeverIncludesOriginalLogic guards the specific privacy/quality
// decision in this package's doc comment: a correction's ORIGINAL (wrong) logic must never reach the
// model, only the corrected version and why.
func TestAssembleGroundingContextNeverIncludesOriginalLogic(t *testing.T) {
	corrections := &fakeCorrectionLister{byWorkspace: map[string][]domain.Correction{
		"ws-1": {
			{ID: "corr-1", WorkspaceID: "ws-1", OriginalLogic: "SELECT * FROM orders", CorrectedLogic: "SELECT * FROM orders WHERE status != 'cancelled'"},
		},
	}}
	got, err := AssembleGroundingContext(context.Background(), "ws-1",
		&fakeRelationshipLister{}, &fakeMetricDefinitionLister{}, corrections)
	if err != nil {
		t.Fatalf("AssembleGroundingContext: %v", err)
	}
	if len(got.Corrections) != 1 {
		t.Fatalf("got %d corrections, want 1", len(got.Corrections))
	}
	if got.Corrections[0].CorrectedLogic != "SELECT * FROM orders WHERE status != 'cancelled'" {
		t.Errorf("corrected_logic = %q, want the corrected form", got.Corrections[0].CorrectedLogic)
	}
	// CorrectionGrounding has no field for the original logic at all — this assertion exists so a
	// future edit that adds one is forced to consciously reconsider this decision rather than
	// silently start including it.
	if _, hasField := reflect.TypeOf(CorrectionGrounding{}).FieldByName("OriginalLogic"); hasField {
		t.Error("CorrectionGrounding must never carry OriginalLogic")
	}
}

func TestAssembleGroundingContextEmptyWorkspaceReturnsEmptySlicesNotNil(t *testing.T) {
	got, err := AssembleGroundingContext(context.Background(), "ws-empty",
		&fakeRelationshipLister{}, &fakeMetricDefinitionLister{}, &fakeCorrectionLister{})
	if err != nil {
		t.Fatalf("AssembleGroundingContext: %v", err)
	}
	if got.Relationships == nil || got.Metrics == nil || got.Corrections == nil {
		t.Errorf("got nil slice(s) for an empty workspace, want empty-but-non-nil so JSON encodes [] not null: %+v", got)
	}
	if len(got.Relationships) != 0 || len(got.Metrics) != 0 || len(got.Corrections) != 0 {
		t.Errorf("got non-empty result for an empty workspace: %+v", got)
	}
}

func TestAssembleGroundingContextPropagatesListErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("relationships", func(t *testing.T) {
		_, err := AssembleGroundingContext(context.Background(), "ws-1",
			&fakeRelationshipLister{err: boom}, &fakeMetricDefinitionLister{}, &fakeCorrectionLister{})
		if err == nil {
			t.Fatal("expected an error when the relationship lister fails")
		}
	})
	t.Run("metrics", func(t *testing.T) {
		_, err := AssembleGroundingContext(context.Background(), "ws-1",
			&fakeRelationshipLister{}, &fakeMetricDefinitionLister{err: boom}, &fakeCorrectionLister{})
		if err == nil {
			t.Fatal("expected an error when the metric definition lister fails")
		}
	})
	t.Run("corrections", func(t *testing.T) {
		_, err := AssembleGroundingContext(context.Background(), "ws-1",
			&fakeRelationshipLister{}, &fakeMetricDefinitionLister{}, &fakeCorrectionLister{err: boom})
		if err == nil {
			t.Fatal("expected an error when the correction lister fails")
		}
	})
}
