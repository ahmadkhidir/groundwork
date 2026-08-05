package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// These tests exercise the SQLite loader against REAL SQLite files rather than a fake execer.
//
// They exist because the original implementation listed tables via `<alias>.sqlite_master`, which
// the sqlite extension does not expose through an attached catalog. It failed for every SQLite
// file, valid or not — and no unit test caught it, because none of them opened a real one. The
// fixtures below are built by DuckDB itself, so verifying this path costs no extra dependency.

// acquireTestHandle leases a real engine handle, since the loaders are meaningful only against an
// actual DuckDB instance — a mocked execer would re-assert the bug rather than detect it.
func acquireTestHandle(t *testing.T) engine.Handle {
	t.Helper()
	provider := engine.NewLocalProvider(time.Minute, testLogger())
	handle, err := provider.Acquire(context.Background(), "ws-loaders")
	if err != nil {
		t.Fatalf("acquiring engine: %v", err)
	}
	t.Cleanup(func() { _ = provider.Release(handle) })
	return handle
}

// writeSQLiteFixture builds a SQLite database containing one table per entry in tables, using
// DuckDB's sqlite extension as the writer.
func writeSQLiteFixture(t *testing.T, h engine.Handle, path string, tables []string) {
	t.Helper()
	ctx := context.Background()

	if _, err := h.Exec(ctx, "INSTALL sqlite_scanner; LOAD sqlite_scanner;"); err != nil {
		t.Skipf("sqlite_scanner extension unavailable (needs network on first install): %v", err)
	}
	if _, err := h.Exec(ctx, "ATTACH "+sqlQuote(path)+" AS fixture (TYPE SQLITE)"); err != nil {
		t.Fatalf("attaching fixture: %v", err)
	}
	for _, table := range tables {
		stmt := "CREATE TABLE fixture." + quoteIdent(table) + " AS SELECT 1 AS id, 'x' AS label"
		if _, err := h.Exec(ctx, stmt); err != nil {
			t.Fatalf("creating fixture table %q: %v", table, err)
		}
	}
	if _, err := h.Exec(ctx, "DETACH fixture"); err != nil {
		t.Fatalf("detaching fixture: %v", err)
	}
}

func TestNormalizeSQLiteIngestsSingleTableFile(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "single.sqlite")
	target := filepath.Join(dir, "out.parquet")

	writeSQLiteFixture(t, handle, source, []string{"sales"})

	if err := normalizeSQLite(context.Background(), handle, source, target); err != nil {
		t.Fatalf("normalizeSQLite() error = %v, want nil", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected Parquet output at %s: %v", target, err)
	}

	// The point of ingest is queryable normalized data, so assert on content rather than on the
	// file merely existing.
	count, err := rowCount(context.Background(), handle, target)
	if err != nil {
		t.Fatalf("rowCount() error = %v", err)
	}
	if count != 1 {
		t.Errorf("normalized row count = %d, want 1", count)
	}
}

func TestNormalizeSQLiteRejectsMultiTableFileWithActionableError(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "multi.sqlite")

	writeSQLiteFixture(t, handle, source, []string{"customers", "orders"})

	err := normalizeSQLite(context.Background(), handle, source, filepath.Join(dir, "out.parquet"))
	if err == nil {
		t.Fatal("normalizeSQLite() error = nil, want a multi-table rejection")
	}

	// FEAT-004 requires failures to be specific and actionable, so assert the message names the
	// tables it found — a bare "ingest failed" would satisfy the type signature and fail the user.
	msg := err.Error()
	for _, want := range []string{"customers", "orders"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not name table %q", msg, want)
		}
	}
	// Guards against the listing regression reappearing: if sqliteTableNames breaks again, the
	// failure is "could not list tables", which is neither actionable nor about table count.
	if strings.Contains(msg, "could not list tables") {
		t.Errorf("table listing itself failed rather than detecting multiple tables: %s", msg)
	}
}

func TestNormalizeToParquetIngestsExcelFile(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "book.xlsx")
	target := filepath.Join(dir, "out.parquet")

	// spatial is used HERE ONLY, to WRITE the fixture: the `excel` extension the production path
	// uses reads xlsx but does not write it, and GDAL's xlsx driver is the simplest way to produce a
	// real spreadsheet without vendoring an Excel library into the test. The ingest path under test
	// must never touch spatial — see normalizeToParquet's doc comment and risk_log.md R-017.
	if _, err := handle.Exec(ctx, "INSTALL spatial; LOAD spatial;"); err != nil {
		t.Skipf("spatial extension unavailable for fixture writing (needs network on first install): %v", err)
	}
	writeFixture := "COPY (SELECT 1 AS id, 'alice' AS name UNION ALL SELECT 2, 'bob') TO " +
		sqlQuote(source) + " WITH (FORMAT GDAL, DRIVER 'xlsx')"
	if _, err := handle.Exec(ctx, writeFixture); err != nil {
		t.Fatalf("writing xlsx fixture: %v", err)
	}

	if err := normalizeToParquet(ctx, handle, domain.FormatExcel, source, target); err != nil {
		t.Fatalf("normalizeToParquet(Excel) error = %v, want nil", err)
	}

	count, err := rowCount(ctx, handle, target)
	if err != nil {
		t.Fatalf("rowCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("normalized row count = %d, want 2", count)
	}
}

func TestNormalizeToParquetRejectsUnsupportedFormat(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()

	err := normalizeToParquet(context.Background(), handle, domain.DatasetFormat("docx"),
		filepath.Join(dir, "in.docx"), filepath.Join(dir, "out.parquet"))
	if err == nil {
		t.Fatal("normalizeToParquet() error = nil, want an unsupported-format error")
	}
	if !strings.Contains(err.Error(), "not a supported dataset format") {
		t.Errorf("error = %q, want it to name the unsupported format", err)
	}
}
