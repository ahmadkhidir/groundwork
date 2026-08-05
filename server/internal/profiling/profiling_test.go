package profiling

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

// acquireTestHandle leases a real engine handle — profiling is meaningful only against an actual
// DuckDB instance (DESCRIBE and approx_count_distinct are real engine behavior, not something a fake
// execer could usefully stand in for), mirroring internal/ingest/loaders_test.go's approach.
func acquireTestHandle(t *testing.T) engine.Handle {
	t.Helper()
	provider := engine.NewLocalProvider(time.Minute, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	handle, err := provider.Acquire(context.Background(), "ws-profiling")
	if err != nil {
		t.Fatalf("acquiring engine: %v", err)
	}
	t.Cleanup(func() { _ = provider.Release(handle) })
	return handle
}

// writeParquetFixture writes selectSQL's result to a Parquet file at path.
func writeParquetFixture(t *testing.T, h engine.Handle, path, selectSQL string) {
	t.Helper()
	stmt := "COPY (" + selectSQL + ") TO '" + path + "' (FORMAT parquet)"
	if _, err := h.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("writing parquet fixture: %v", err)
	}
}

func TestProfileComputesDistinctCountNullRateAndInferredType(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.parquet")

	// 4 rows: id is fully populated and unique (integer); total is numeric with one null;
	// status is a string with a repeated value (low cardinality).
	writeParquetFixture(t, handle, path, `
		SELECT * FROM (VALUES
			(1, 42.5, 'shipped'),
			(2, 10.0, 'shipped'),
			(3, NULL, 'pending'),
			(4, 7.25, 'pending')
		) AS t(id, total, status)
	`)

	stats, err := Profile(context.Background(), handle, path)
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("got %d columns, want 3", len(stats))
	}

	byName := make(map[string]ColumnStat, len(stats))
	for _, s := range stats {
		byName[s.Name] = s
	}

	id, ok := byName["id"]
	if !ok {
		t.Fatal("missing column 'id'")
	}
	if id.InferredType != domain.TypeInteger {
		t.Errorf("id.InferredType = %q, want %q", id.InferredType, domain.TypeInteger)
	}
	if id.NullRate != 0 {
		t.Errorf("id.NullRate = %v, want 0", id.NullRate)
	}
	if id.DistinctCount != 4 {
		t.Errorf("id.DistinctCount = %d, want 4", id.DistinctCount)
	}

	total, ok := byName["total"]
	if !ok {
		t.Fatal("missing column 'total'")
	}
	if total.InferredType != domain.TypeNumber {
		t.Errorf("total.InferredType = %q, want %q", total.InferredType, domain.TypeNumber)
	}
	if got, want := total.NullRate, 0.25; got != want {
		t.Errorf("total.NullRate = %v, want %v (1 of 4 rows is null)", got, want)
	}

	status, ok := byName["status"]
	if !ok {
		t.Fatal("missing column 'status'")
	}
	if status.InferredType != domain.TypeString {
		t.Errorf("status.InferredType = %q, want %q", status.InferredType, domain.TypeString)
	}
	if status.DistinctCount != 2 {
		t.Errorf("status.DistinctCount = %d, want 2 (shipped, pending)", status.DistinctCount)
	}

	for _, s := range stats {
		if s.TypeConfidence <= 0 || s.TypeConfidence > 1 {
			t.Errorf("%s.TypeConfidence = %v, want in (0,1]", s.Name, s.TypeConfidence)
		}
	}
}

func TestProfileRecognizesBooleanDateAndTimestamp(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "typed.parquet")

	writeParquetFixture(t, handle, path, `
		SELECT * FROM (VALUES
			(true, DATE '2024-01-01', TIMESTAMP '2024-01-01 10:00:00'),
			(false, DATE '2024-01-02', TIMESTAMP '2024-01-02 11:00:00')
		) AS t(is_active, signup_date, last_login)
	`)

	stats, err := Profile(context.Background(), handle, path)
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}

	want := map[string]domain.InferredType{
		"is_active":   domain.TypeBoolean,
		"signup_date": domain.TypeDate,
		"last_login":  domain.TypeTimestamp,
	}
	for _, s := range stats {
		if wantType, ok := want[s.Name]; ok && s.InferredType != wantType {
			t.Errorf("%s.InferredType = %q, want %q", s.Name, s.InferredType, wantType)
		}
	}
}

// Band thresholds from schema.md / FEAT-005, restated here rather than imported from production code
// so a change to a production constant cannot silently move the bar these assertions check against.
// The web tier derives the same bands in web/src/components/confirmation/types.ts.
const (
	highConfidenceBand   = 0.85
	mediumConfidenceBand = 0.6
)

func profileByName(t *testing.T, h engine.Handle, path string) map[string]ColumnStat {
	t.Helper()
	stats, err := Profile(context.Background(), h, path)
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	byName := make(map[string]ColumnStat, len(stats))
	for _, s := range stats {
		byName[s.Name] = s
	}
	return byName
}

// TestTypeConfidenceDifferentiatesCleanFromAmbiguousColumns is the behavioral contract FEAT-005 now
// specifies: type_confidence measures type-inference AGREEMENT across non-null values, not which
// type the sniffer picked. The previous fixed-prior heuristic put every column at or above the HIGH
// threshold — string sat exactly on 0.85 — so the badge on screens/07 told the user which type was
// chosen rather than how much evidence there was.
//
// Deliberately asserts bands and relative ordering, not magic constants: FEAT-005 fixes the bands and
// what the number measures, while leaving sample size, coercion rules, and the floor tunable.
func TestTypeConfidenceDifferentiatesCleanFromAmbiguousColumns(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.parquet")

	// Four columns over 10 rows:
	//   clean_id       — a clean integer column; every value coerces, so agreement is total.
	//   free_text      — genuine free text; none of it coerces to a stricter type the sniffer rejected.
	//   mostly_numeric — 9 of 10 values are numbers held as text; the sniffer had real, unresolved
	//                    ambiguity about whether this should have been a number.
	//   part_numeric   — 2 of 10 numeric-looking; ambiguous enough to leave HIGH, not enough for LOW.
	writeParquetFixture(t, handle, path, `
		SELECT * FROM (VALUES
			(1,  'shipped',   '100',  'shipped'),
			(2,  'pending',   '200',  'pending'),
			(3,  'cancelled', '300',  'cancelled'),
			(4,  'refunded',  '400',  'refunded'),
			(5,  'shipped',   '500',  'shipped'),
			(6,  'pending',   '600',  'pending'),
			(7,  'cancelled', '700',  'cancelled'),
			(8,  'refunded',  '800',  'refunded'),
			(9,  'shipped',   '900',  '42'),
			(10, 'pending',   'n/a',  '43')
		) AS t(clean_id, free_text, mostly_numeric, part_numeric)
	`)

	byName := profileByName(t, handle, path)
	for _, name := range []string{"clean_id", "free_text", "mostly_numeric", "part_numeric"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing column %q in profile result", name)
		}
	}

	cleanID := byName["clean_id"]
	if cleanID.InferredType != domain.TypeInteger {
		t.Fatalf("clean_id.InferredType = %q, want %q", cleanID.InferredType, domain.TypeInteger)
	}
	if cleanID.TypeConfidence < highConfidenceBand {
		t.Errorf("clean_id.TypeConfidence = %v, want >= %v — a clean integer column has total type agreement",
			cleanID.TypeConfidence, highConfidenceBand)
	}

	freeText := byName["free_text"]
	if freeText.TypeConfidence < highConfidenceBand {
		t.Errorf("free_text.TypeConfidence = %v, want >= %v — genuine free text matches no stricter type, so string is unambiguous",
			freeText.TypeConfidence, highConfidenceBand)
	}

	// The regression this change exists for: under the old fixed prior this scored 0.85 — HIGH —
	// purely because the sniffer said "string".
	mostlyNumeric := byName["mostly_numeric"]
	if mostlyNumeric.InferredType != domain.TypeString {
		t.Fatalf("mostly_numeric.InferredType = %q, want %q", mostlyNumeric.InferredType, domain.TypeString)
	}
	if mostlyNumeric.TypeConfidence >= highConfidenceBand {
		t.Errorf("mostly_numeric.TypeConfidence = %v, want < %v — a mostly-numeric text column is exactly the ambiguity a reviewer needs surfaced",
			mostlyNumeric.TypeConfidence, highConfidenceBand)
	}

	partNumeric := byName["part_numeric"]
	if partNumeric.TypeConfidence >= highConfidenceBand {
		t.Errorf("part_numeric.TypeConfidence = %v, want < %v", partNumeric.TypeConfidence, highConfidenceBand)
	}
	if partNumeric.TypeConfidence < mediumConfidenceBand {
		t.Errorf("part_numeric.TypeConfidence = %v, want >= %v — mild ambiguity should read MEDIUM, not LOW",
			partNumeric.TypeConfidence, mediumConfidenceBand)
	}

	// The whole point of the change: the badges differentiate rather than all reading HIGH.
	if !(freeText.TypeConfidence > partNumeric.TypeConfidence &&
		partNumeric.TypeConfidence > mostlyNumeric.TypeConfidence) {
		t.Errorf("confidence does not decrease with ambiguity: free_text=%v part_numeric=%v mostly_numeric=%v",
			freeText.TypeConfidence, partNumeric.TypeConfidence, mostlyNumeric.TypeConfidence)
	}
}

// TestTypeConfidenceIsWeakenedByNullRate holds the half of FEAT-005's heuristic that survived the
// change: a mostly-empty column gave the sniffer little to work with, so agreement alone overstates
// the evidence.
func TestTypeConfidenceIsWeakenedByNullRate(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.parquet")

	// Both columns coerce perfectly on every value they have; they differ only in how many they have.
	writeParquetFixture(t, handle, path, `
		SELECT * FROM (VALUES
			(1, 1),
			(2, NULL),
			(3, NULL),
			(4, NULL)
		) AS t(dense, sparse)
	`)

	byName := profileByName(t, handle, path)
	dense, sparse := byName["dense"], byName["sparse"]
	if sparse.NullRate != 0.75 {
		t.Fatalf("sparse.NullRate = %v, want 0.75", sparse.NullRate)
	}
	if sparse.TypeConfidence >= dense.TypeConfidence {
		t.Errorf("sparse.TypeConfidence = %v, want < dense's %v — a mostly-empty column is weaker evidence",
			sparse.TypeConfidence, dense.TypeConfidence)
	}
}

// TestTypeConfidenceFloorsAnAllNullColumn asserts the floor FEAT-005 keeps: no evidence at all still
// produces a real, low number rather than a ~0 that reads as "no opinion".
func TestTypeConfidenceFloorsAnAllNullColumn(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-col.parquet")

	writeParquetFixture(t, handle, path, `
		SELECT * FROM (VALUES
			(1, CAST(NULL AS VARCHAR)),
			(2, CAST(NULL AS VARCHAR))
		) AS t(id, notes)
	`)

	byName := profileByName(t, handle, path)
	notes := byName["notes"]
	if notes.TypeConfidence <= 0 {
		t.Errorf("notes.TypeConfidence = %v, want a positive floored value", notes.TypeConfidence)
	}
	if notes.TypeConfidence >= mediumConfidenceBand {
		t.Errorf("notes.TypeConfidence = %v, want < %v — an all-null column is LOW confidence",
			notes.TypeConfidence, mediumConfidenceBand)
	}
}

func TestProfileRejectsAnEmptyColumnSet(t *testing.T) {
	// A Parquet file must have at least one column; this asserts Profile fails honestly rather than
	// returning an empty, silently-successful result for a malformed file. There's no direct way to
	// write a zero-column Parquet through COPY, so this instead points at a nonexistent path, which
	// surfaces through the same "no columns" code path DESCRIBE would hit on failure.
	handle := acquireTestHandle(t)
	_, err := Profile(context.Background(), handle, filepath.Join(t.TempDir(), "does-not-exist.parquet"))
	if err == nil {
		t.Fatal("Profile() error = nil, want an error for a nonexistent file")
	}
}

// TestDistinctCountIsExactNotApproximate guards against reintroducing approx_count_distinct.
//
// distinct_count is shown to the user and feeds FEAT-008's near-unique (>=0.95) identifier test,
// where an error can flip a column between "identifier" and "measure" and change a candidate join's
// confidence band. HyperLogLog is approximate by construction: measured against this driver it
// returned 48 for 50 distinct values and 577 for 500 — a 15% overcount, enough to push a genuinely
// non-unique column past the near-unique threshold.
//
// 500 distinct values is deliberate. A handful of values estimates exactly, so a small fixture
// passes whether the implementation is exact or approximate and would guard nothing.
func TestDistinctCountIsExactNotApproximate(t *testing.T) {
	h := acquireTestHandle(t)
	path := filepath.Join(t.TempDir(), "distinct.parquet")
	writeParquetFixture(t, h, path, "SELECT i + 0.5 AS total FROM range(500) t(i)")

	stats := profileByName(t, h, path)
	if got := stats["total"].DistinctCount; got != 500 {
		t.Errorf("DistinctCount = %d, want exactly 500 — an approximate count is not acceptable for a "+
			"user-visible statistic that also drives identifier classification", got)
	}
}
