package suggestions

import (
	"context"
	"strings"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeSource is an in-memory Source for the generator tests.
type fakeSource struct {
	datasets      []domain.Dataset
	columns       []domain.Column
	relationships []domain.Relationship
	metrics       []domain.MetricDefinition
}

func (f *fakeSource) Datasets(_ context.Context, _ string) ([]domain.Dataset, error) {
	return f.datasets, nil
}
func (f *fakeSource) Columns(_ context.Context, _ string) ([]domain.Column, error) {
	return f.columns, nil
}
func (f *fakeSource) Relationships(_ context.Context, _ string) ([]domain.Relationship, error) {
	return f.relationships, nil
}
func (f *fakeSource) Metrics(_ context.Context, _ string) ([]domain.MetricDefinition, error) {
	return f.metrics, nil
}

func col(id, datasetID, name string, t domain.InferredType) domain.Column {
	return domain.Column{ID: id, DatasetID: datasetID, Name: name, InferredType: t}
}

func TestGenerateProducesTypeAwareQuestions(t *testing.T) {
	src := &fakeSource{
		datasets: []domain.Dataset{
			{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete, RowCount: 100},
			{ID: "ds-2", OriginalFilename: "orders.csv", IngestStatus: domain.IngestFailed}, // excluded: not complete
		},
		columns: []domain.Column{
			col("c-1", "ds-1", "customer_id", domain.TypeString),
			col("c-2", "ds-1", "total_amount", domain.TypeNumber),
			col("c-3", "ds-1", "order_date", domain.TypeDate),
			col("c-4", "ds-1", "email", domain.TypeString),
			col("c-5", "ds-2", "amount", domain.TypeNumber), // incomplete dataset → ignored
		},
		metrics: []domain.MetricDefinition{
			{ID: "m-1", Name: "revenue", SQLExpression: "SUM(total_amount)"},
		},
		relationships: []domain.Relationship{
			{ID: "r-1", SourceColumnID: "c-1", TargetColumnID: "c-1"},
		},
	}
	// c-4 is PII-flagged — must never appear, even by name.
	src.columns[3].PIIFlag = true

	got, err := Generate(context.Background(), "ws-1", src)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(got) < 5 {
		t.Errorf("got %d suggestions, want at least 5 for a data-rich workspace:\n%v", len(got), got)
	}
	if len(got) > maxSuggestions {
		t.Errorf("got %d suggestions, want at most %d", len(got), maxSuggestions)
	}

	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"What is the total total amount?",
		"What is the average total amount?",
		"What is the total amount for each customer id?",
		"What are the top customer id by total amount?",
		"How does total amount change over time?",
		"What are the most common customer id values?",
		"What is the revenue?",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("suggestions missing %q\n%v", want, got)
		}
	}
	if strings.Contains(joined, "email") {
		t.Errorf("a PII-flagged column was referenced by name: %v", got)
	}
	if strings.Contains(joined, "SQL") || strings.Contains(joined, "SELECT") {
		t.Errorf("suggestion contains SQL terminology: %v", got)
	}
	for _, s := range got {
		if strings.ContainsAny(s, "0123456789") && strings.Contains(s, "total_amount") {
			// No template should assert a computed number as a fact (the only digits allowed are
			// none — these templates never state quantities).
			t.Errorf("suggestion appears to assert a number: %q", s)
		}
	}
}

func TestGenerateNeverReferencesUnsafeOrNonexistentEntities(t *testing.T) {
	src := &fakeSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "x.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			col("c-1", "ds-1", "amount", domain.TypeNumber),
			col("c-2", "ds-1", "ssn", domain.TypeString),
		},
		relationships: []domain.Relationship{
			// Target column does not exist in the pool — must be skipped, never phrased.
			{ID: "r-1", SourceColumnID: "c-1", TargetColumnID: "missing"},
			// Source is the PII column — the whole relationship must be skipped.
			{ID: "r-2", SourceColumnID: "c-2", TargetColumnID: "c-1"},
		},
	}
	src.columns[1].PIIFlag = true

	got, err := Generate(context.Background(), "ws-1", src)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "ssn") {
		t.Errorf("a PII-flagged column was referenced: %v", got)
	}
	if strings.Contains(joined, "missing") {
		t.Errorf("a nonexistent column was referenced: %v", got)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	src := &fakeSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "x.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			col("c-2", "ds-1", "amount", domain.TypeNumber),
			col("c-1", "ds-1", "region", domain.TypeString),
		},
	}
	ctx := context.Background()
	first, err := Generate(ctx, "ws-1", src)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := Generate(ctx, "ws-1", src)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("regeneration changed length: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("regeneration changed order/content at %d: %q vs %q", i, first[i], second[i])
		}
	}
}

func TestGenerateReturnsEmptyForEmptyWorkspace(t *testing.T) {
	got, err := Generate(context.Background(), "ws-1", &fakeSource{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty for a workspace with no data", got)
	}
}

func TestGeneratePhrasesConfirmedRelationshipsWithResolvedNames(t *testing.T) {
	src := &fakeSource{
		datasets: []domain.Dataset{
			{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete},
			{ID: "ds-2", OriginalFilename: "orders.csv", IngestStatus: domain.IngestComplete},
		},
		columns: []domain.Column{
			col("c-1", "ds-1", "customer_id", domain.TypeString),
			col("c-2", "ds-2", "customer_id", domain.TypeString),
		},
		relationships: []domain.Relationship{
			{ID: "r-1", SourceColumnID: "c-1", TargetColumnID: "c-2"},
		},
	}
	got, err := Generate(context.Background(), "ws-1", src)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "How do customer id and customer id relate across the linked datasets?") {
		t.Errorf("confirmed relationship not phrased with resolved names:\n%v", got)
	}
}
