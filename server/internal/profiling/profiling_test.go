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
