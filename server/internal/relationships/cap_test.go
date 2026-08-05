package relationships

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// highConfidenceBand is schema.md's HIGH threshold, restated here rather than imported from
// production code so a change to a production constant cannot silently move the bar these
// assertions check against.
const highConfidenceBand = 0.85

func findCandidate(candidates []domain.CandidateJoin, sourceID, targetID string) *domain.CandidateJoin {
	for i := range candidates {
		if candidates[i].SourceColumnID == sourceID && candidates[i].TargetColumnID == targetID {
			return &candidates[i]
		}
	}
	return nil
}

// TestInferCapsValueOverlapOnlyNumericJoinAtMedium reproduces the live false positive that motivated
// FEAT-008's cap: two unrelated money columns from different files — an invoice `total` and a payment
// `amount` — offered as a real relationship purely because their values overlapped.
//
// FEAT-008: "a candidate join whose only meaningful evidence is value overlap on a column that is not
// identifier-shaped... must be capped at MEDIUM, never reported as HIGH", and its basis must state
// that the cap was applied.
func TestInferCapsValueOverlapOnlyNumericJoinAtMedium(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	invoicesPath := filepath.Join(dir, "invoices.parquet")
	paymentsPath := filepath.Join(dir, "payments.parquet")

	// Identical value sets: the strongest possible overlap signal, which is exactly the evidence the
	// cap exists to refuse to treat as strong on its own.
	writeParquetFixture(t, handle, invoicesPath,
		`SELECT CAST(v AS DOUBLE) AS total FROM generate_series(1, 20) AS t(v)`)
	writeParquetFixture(t, handle, paymentsPath,
		`SELECT CAST(v AS DOUBLE) AS amount FROM generate_series(1, 20) AS t(v)`)

	invoices := DatasetInput{
		DatasetID: "ds-invoices", ParquetPath: invoicesPath, RowCount: 20,
		// 15 distinct of 20 rows: a plausible money column, and well short of the near-unique shape
		// that would make it read as a key.
		Columns: []domain.Column{
			{ID: "col-total", Name: "total", InferredType: domain.TypeNumber, DistinctCount: 15},
		},
	}
	payments := DatasetInput{
		DatasetID: "ds-payments", ParquetPath: paymentsPath, RowCount: 20,
		Columns: []domain.Column{
			{ID: "col-amount", Name: "amount", InferredType: domain.TypeNumber, DistinctCount: 15},
		},
	}

	candidates, err := Infer(context.Background(), handle, invoices, payments)
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}

	found := findCandidate(candidates, "col-total", "col-amount")
	if found == nil {
		t.Fatalf("expected the total/amount pair to be evaluated and persisted, got %+v", candidates)
	}
	if found.ConfidenceScore >= highConfidenceBand {
		t.Errorf("confidence_score = %v, want < %v — value overlap alone between two money columns must never report HIGH",
			found.ConfidenceScore, highConfidenceBand)
	}
	if !strings.Contains(found.Basis, "capped") {
		t.Errorf("basis = %q, want it to state that the cap was applied", found.Basis)
	}
}

// TestInferCapClampsAScoreThatWouldOtherwiseReadHigh is the regression that actually exercises the
// clamp rather than a pair that fell short of HIGH anyway: two similarly-named, non-identifier
// numeric measure columns whose combined signal reaches the HIGH band before the cap and must land
// in MEDIUM after it.
func TestInferCapClampsAScoreThatWouldOtherwiseReadHigh(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	aPath := filepath.Join(dir, "orders_a.parquet")
	bPath := filepath.Join(dir, "orders_b.parquet")
	writeParquetFixture(t, handle, aPath,
		`SELECT CAST(v AS DOUBLE) AS total_value FROM generate_series(1, 50) AS t(v)`)
	writeParquetFixture(t, handle, bPath,
		`SELECT CAST(v AS DOUBLE) AS total_price FROM generate_series(1, 50) AS t(v)`)

	a := DatasetInput{
		DatasetID: "ds-a", ParquetPath: aPath, RowCount: 50,
		// 47/50 = 0.94 — high, but below UniqueCardinalityThreshold, so still a measure, not a key.
		Columns: []domain.Column{
			{ID: "col-total-value", Name: "total_value", InferredType: domain.TypeNumber, DistinctCount: 47},
		},
	}
	b := DatasetInput{
		DatasetID: "ds-b", ParquetPath: bPath, RowCount: 50,
		Columns: []domain.Column{
			{ID: "col-total-price", Name: "total_price", InferredType: domain.TypeNumber, DistinctCount: 47},
		},
	}

	candidates, err := Infer(context.Background(), handle, a, b)
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}

	found := findCandidate(candidates, "col-total-value", "col-total-price")
	if found == nil {
		t.Fatalf("expected a candidate join between total_value and total_price, got %+v", candidates)
	}
	if found.ConfidenceScore != ValueOverlapOnlyCeiling {
		t.Errorf("confidence_score = %v, want exactly %v — uncapped this pair scores in the HIGH band, so the cap must clamp it",
			found.ConfidenceScore, ValueOverlapOnlyCeiling)
	}
	if found.ConfidenceScore >= highConfidenceBand {
		t.Errorf("confidence_score = %v landed in the HIGH band despite the cap", found.ConfidenceScore)
	}
	if !strings.Contains(found.Basis, "capped") {
		t.Errorf("basis = %q, want it to state that the cap was applied", found.Basis)
	}
}

// TestInferDoesNotCapAGenuineIdentifierJoin is the other half of the cap's contract. A cap that also
// suppresses real keys is worse than no cap: surfacing genuine joins is FEAT-008's entire purpose,
// and customer_id -> customer_id is the canonical one.
func TestInferDoesNotCapAGenuineIdentifierJoin(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	customersPath := filepath.Join(dir, "customers.parquet")
	ordersPath := filepath.Join(dir, "orders.parquet")
	writeParquetFixture(t, handle, customersPath,
		`SELECT v AS customer_id FROM generate_series(1, 50) AS t(v)`)
	writeParquetFixture(t, handle, ordersPath,
		`SELECT (v % 30) + 1 AS customer_id FROM generate_series(1, 50) AS t(v)`)

	customers := DatasetInput{
		DatasetID: "ds-customers", ParquetPath: customersPath, RowCount: 50,
		Columns: []domain.Column{
			{ID: "col-customers-customer-id", Name: "customer_id", InferredType: domain.TypeInteger, DistinctCount: 50},
		},
	}
	orders := DatasetInput{
		DatasetID: "ds-orders", ParquetPath: ordersPath, RowCount: 50,
		Columns: []domain.Column{
			{ID: "col-orders-customer-id", Name: "customer_id", InferredType: domain.TypeInteger, DistinctCount: 30},
		},
	}

	candidates, err := Infer(context.Background(), handle, customers, orders)
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}

	found := findCandidate(candidates, "col-customers-customer-id", "col-orders-customer-id")
	if found == nil {
		t.Fatalf("expected a candidate join between customer_id and customer_id, got %+v", candidates)
	}
	if found.ConfidenceScore < highConfidenceBand {
		t.Errorf("confidence_score = %v, want >= %v — a real identifier join must still be able to report HIGH",
			found.ConfidenceScore, highConfidenceBand)
	}
	if strings.Contains(found.Basis, "capped") {
		t.Errorf("basis = %q claims the cap applied to a genuine identifier join", found.Basis)
	}
}

// TestInferDoesNotCapCodeNamedNumericColumns guards the name half of "identifier-shaped": FEAT-008
// counts a `_code` column as an identifier even when its values repeat heavily, so it is exempt from
// the cap despite being numeric and nowhere near unique.
func TestInferDoesNotCapCodeNamedNumericColumns(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	aPath := filepath.Join(dir, "branches.parquet")
	bPath := filepath.Join(dir, "outlets.parquet")
	writeParquetFixture(t, handle, aPath,
		`SELECT (v % 8) + 1 AS branch_code FROM generate_series(1, 40) AS t(v)`)
	writeParquetFixture(t, handle, bPath,
		`SELECT (v % 8) + 1 AS outlet_code FROM generate_series(1, 40) AS t(v)`)

	a := DatasetInput{
		DatasetID: "ds-branches", ParquetPath: aPath, RowCount: 40,
		Columns: []domain.Column{
			{ID: "col-branch-code", Name: "branch_code", InferredType: domain.TypeInteger, DistinctCount: 8},
		},
	}
	b := DatasetInput{
		DatasetID: "ds-outlets", ParquetPath: bPath, RowCount: 40,
		Columns: []domain.Column{
			{ID: "col-outlet-code", Name: "outlet_code", InferredType: domain.TypeInteger, DistinctCount: 8},
		},
	}

	candidates, err := Infer(context.Background(), handle, a, b)
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}

	found := findCandidate(candidates, "col-branch-code", "col-outlet-code")
	if found == nil {
		t.Fatalf("expected a candidate join between branch_code and outlet_code, got %+v", candidates)
	}
	if strings.Contains(found.Basis, "capped") {
		t.Errorf("basis = %q applied the cap to a code-named column, which FEAT-008 calls identifier-shaped", found.Basis)
	}
}

func TestIdentifierShapedRecognizesIdKeyAndCodeShapes(t *testing.T) {
	cases := []struct {
		name           string
		column         domain.Column
		rowCount       int64
		wantIdentifier bool
	}{
		{"bare id", domain.Column{Name: "id", DistinctCount: 1}, 100, true},
		{"suffixed id", domain.Column{Name: "customer_id", DistinctCount: 1}, 100, true},
		{"prefixed id", domain.Column{Name: "id_customer", DistinctCount: 1}, 100, true},
		{"key suffix", domain.Column{Name: "order_key", DistinctCount: 1}, 100, true},
		{"code suffix", domain.Column{Name: "branch_code", DistinctCount: 1}, 100, true},
		{"near-unique column", domain.Column{Name: "reference_number", DistinctCount: 99}, 100, true},
		{"money column", domain.Column{Name: "total", DistinctCount: 40}, 100, false},
		{"amount column", domain.Column{Name: "amount", DistinctCount: 40}, 100, false},
		// The substring trap: "paid" and "void" both contain "id", and are a measure and a flag —
		// exempting them from the cap would defeat it for exactly the columns it targets.
		{"paid is not an id", domain.Column{Name: "paid", DistinctCount: 2}, 100, false},
		{"void is not an id", domain.Column{Name: "void", DistinctCount: 2}, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identifierShaped(tc.column, tc.rowCount); got != tc.wantIdentifier {
				t.Errorf("identifierShaped(%q) = %v, want %v", tc.column.Name, got, tc.wantIdentifier)
			}
		})
	}
}
