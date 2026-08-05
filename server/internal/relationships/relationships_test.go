package relationships

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// acquireTestHandle leases a real engine handle — relationship inference's value-overlap sampling is
// meaningful only against an actual DuckDB instance, mirroring internal/profiling's own tests.
func acquireTestHandle(t *testing.T) engine.Handle {
	t.Helper()
	provider := engine.NewLocalProvider(time.Minute, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	handle, err := provider.Acquire(context.Background(), "ws-relationships")
	if err != nil {
		t.Fatalf("acquiring engine: %v", err)
	}
	t.Cleanup(func() { _ = provider.Release(handle) })
	return handle
}

func writeParquetFixture(t *testing.T, h engine.Handle, path, selectSQL string) {
	t.Helper()
	stmt := "COPY (" + selectSQL + ") TO '" + path + "' (FORMAT parquet)"
	if _, err := h.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("writing parquet fixture: %v", err)
	}
}

func TestInferFindsAHighConfidenceForeignKeyMatch(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	customersPath := filepath.Join(dir, "customers.parquet")
	ordersPath := filepath.Join(dir, "orders.parquet")

	writeParquetFixture(t, handle, customersPath, `
		SELECT * FROM (VALUES (1, 'Ada'), (2, 'Grace'), (3, 'Alan')) AS t(customer_id, name)
	`)
	writeParquetFixture(t, handle, ordersPath, `
		SELECT * FROM (VALUES (100, 1), (101, 2), (102, 1)) AS t(order_id, customer_id)
	`)

	customers := DatasetInput{
		DatasetID: "ds-customers", ParquetPath: customersPath, RowCount: 3,
		Columns: []domain.Column{
			{ID: "col-customer-id", Name: "customer_id", InferredType: domain.TypeInteger, DistinctCount: 3},
			{ID: "col-name", Name: "name", InferredType: domain.TypeString, DistinctCount: 3},
		},
	}
	orders := DatasetInput{
		DatasetID: "ds-orders", ParquetPath: ordersPath, RowCount: 3,
		Columns: []domain.Column{
			{ID: "col-order-id", Name: "order_id", InferredType: domain.TypeInteger, DistinctCount: 3},
			{ID: "col-order-customer-id", Name: "customer_id", InferredType: domain.TypeInteger, DistinctCount: 2},
		},
	}

	candidates, err := Infer(context.Background(), handle, customers, orders)
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}

	var found *domain.CandidateJoin
	for i := range candidates {
		if candidates[i].SourceColumnID == "col-customer-id" && candidates[i].TargetColumnID == "col-order-customer-id" {
			found = &candidates[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a candidate join between customer_id and customer_id, got %+v", candidates)
	}
	if found.ConfidenceScore < MinConfidenceToPersist {
		t.Errorf("confidence_score = %v, want >= %v for an exact-name, fully-overlapping, compatible-type match", found.ConfidenceScore, MinConfidenceToPersist)
	}
	if found.Basis == "" {
		t.Error("basis is empty — a confidence number alone is not an explanation")
	}
	if found.Status != domain.JoinPending {
		t.Errorf("status = %q, want %q — never auto-applied", found.Status, domain.JoinPending)
	}

	// order_id (unique, unrelated name) vs name (string, unrelated) should not appear as a candidate.
	for _, c := range candidates {
		if c.SourceColumnID == "col-name" && c.TargetColumnID == "col-order-id" {
			t.Errorf("unexpected weak candidate between unrelated columns: %+v", c)
		}
	}
}

func TestInferSkipsUnrelatedColumnPairs(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	aPath := filepath.Join(dir, "a.parquet")
	bPath := filepath.Join(dir, "b.parquet")
	writeParquetFixture(t, handle, aPath, `SELECT * FROM (VALUES ('red'), ('blue')) AS t(favorite_color)`)
	writeParquetFixture(t, handle, bPath, `SELECT * FROM (VALUES (99.5), (12.25)) AS t(shipping_weight_kg)`)

	a := DatasetInput{
		DatasetID: "ds-a", ParquetPath: aPath, RowCount: 2,
		Columns: []domain.Column{{ID: "col-a", Name: "favorite_color", InferredType: domain.TypeString, DistinctCount: 2}},
	}
	b := DatasetInput{
		DatasetID: "ds-b", ParquetPath: bPath, RowCount: 2,
		Columns: []domain.Column{{ID: "col-b", Name: "shipping_weight_kg", InferredType: domain.TypeNumber, DistinctCount: 2}},
	}

	candidates, err := Infer(context.Background(), handle, a, b)
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("got %d candidates for two unrelated columns, want 0: %+v", len(candidates), candidates)
	}
}

func TestNameSimilarityRecognizesExactAndForeignKeyShapes(t *testing.T) {
	cases := []struct {
		a, b string
		min  float64
	}{
		{"customer_id", "customer_id", 1.0},
		{"Customer ID", "customer_id", 1.0},
		{"customer_id", "id", 0.8},
		{"id", "customer_id", 0.8},
	}
	for _, c := range cases {
		got := nameSimilarity(c.a, c.b)
		if got < c.min {
			t.Errorf("nameSimilarity(%q, %q) = %v, want >= %v", c.a, c.b, got, c.min)
		}
	}
}

func TestTypeCompatibilityScoresRelatedTypesHigher(t *testing.T) {
	if got := typeCompatibility(domain.TypeInteger, domain.TypeInteger); got != 1 {
		t.Errorf("identical types = %v, want 1", got)
	}
	if got := typeCompatibility(domain.TypeInteger, domain.TypeNumber); got < 0.5 {
		t.Errorf("integer/number = %v, want a high compatibility score", got)
	}
	if got := typeCompatibility(domain.TypeString, domain.TypeBoolean); got != 0 {
		t.Errorf("string/boolean = %v, want 0", got)
	}
}
