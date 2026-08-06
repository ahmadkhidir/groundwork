package ingest

import (
	"context"
	"fmt"
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

// parquetColumnTypes runs DESCRIBE over a Parquet file and returns column name → DuckDB type.
func parquetColumnTypes(t *testing.T, handle engine.Handle, path string) map[string]string {
	t.Helper()
	rows, err := handle.Query(context.Background(), "DESCRIBE SELECT * FROM read_parquet('"+path+"')")
	if err != nil {
		t.Fatalf("DESCRIBE parquet %s: %v", path, err)
	}
	defer rows.Close()

	cols := len(rows.Columns())
	if cols == 0 {
		cols = 6
	}
	out := map[string]string{}
	for rows.Next() {
		dest := make([]any, cols)
		ptrs := make([]any, cols)
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan DESCRIBE parquet %s: %v", path, err)
		}
		out[fmt.Sprintf("%v", dest[0])] = fmt.Sprintf("%v", dest[1])
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate DESCRIBE parquet %s: %v", path, err)
	}
	return out
}

// TestNormalizeCSVConvertsDDMonYYDates is the regression test for Option C (dd-mmm-yy date support):
// DuckDB's sniffer reads such columns as VARCHAR, which left date columns inferred as Text with a
// floored 40% confidence badge and made date math fail at query time. The normalizer must turn a
// column whose every non-null value parses into a real DATE in the normalized Parquet.
func TestNormalizeCSVConvertsDDMonYYDates(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "dates.csv")
	target := filepath.Join(dir, "out.parquet")

	// Covers the user's real shapes: two-digit years, four-digit years, single-digit days,
	// and lowercase month names, all mixed in one column.
	csv := "acct_date_open,amount,label\n" +
		"29-Aug-18,1,first\n" +
		"1-Nov-18,2,second\n" +
		"31-Dec-08,3,third\n" +
		"31-Dec-2008,4,fourth\n" +
		"1-nov-18,5,fifth\n"
	if err := os.WriteFile(source, []byte(csv), 0o644); err != nil {
		t.Fatalf("writing csv fixture: %v", err)
	}

	if err := normalizeToParquet(ctx, handle, domain.FormatCSV, source, target); err != nil {
		t.Fatalf("normalizeToParquet(CSV) error = %v", err)
	}

	types := parquetColumnTypes(t, handle, target)
	if got := types["acct_date_open"]; got != "DATE" {
		t.Errorf("acct_date_open normalized type = %q, want DATE", got)
	}

	// Values survive the conversion exactly — nothing silently nulled or shifted.
	rows, err := handle.Query(ctx, "SELECT strftime(acct_date_open, '%Y-%m-%d') FROM read_parquet('"+target+"') ORDER BY amount")
	if err != nil {
		t.Fatalf("reading normalized dates: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scanning normalized date: %v", err)
		}
		got = append(got, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating normalized dates: %v", err)
	}
	want := []string{"2018-08-29", "2018-11-01", "2008-12-31", "2008-12-31", "2018-11-01"}
	if !slicesEqual(got, want) {
		t.Errorf("normalized dates = %v, want %v", got, want)
	}
}

// TestNormalizeCSVKeepsAmbiguousColumnsAsText pins the conservative side of the CSV normalizer: a
// column stays VARCHAR — never silently nulled — unless every non-null value actually parses as a
// dd-mmm-yy date. That keeps ambiguous data visible to the type-review step instead of quietly
// corrupting it (the same philosophy as normalizeExcel's fallback).
func TestNormalizeCSVKeepsAmbiguousColumnsAsText(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "mixed.csv")
	target := filepath.Join(dir, "out.parquet")

	csv := "amount,when_note,empty_dates,mobile_no\n" +
		"1,29-Aug-18,,8034720413\n" +
		"2,n/a,,0401234567\n" +
		"3,1-Nov-18,,9911122233\n"
	if err := os.WriteFile(source, []byte(csv), 0o644); err != nil {
		t.Fatalf("writing csv fixture: %v", err)
	}

	if err := normalizeToParquet(ctx, handle, domain.FormatCSV, source, target); err != nil {
		t.Fatalf("normalizeToParquet(CSV) error = %v", err)
	}

	types := parquetColumnTypes(t, handle, target)

	// The date-looking column has one unparseable value ("n/a"): the whole column stays text,
	// so the review step still sees the low fit rate instead of losing the value.
	if got := types["when_note"]; got != "VARCHAR" {
		t.Errorf("when_note type = %q, want VARCHAR (mixed values must not convert)", got)
	}

	// An all-empty column converts on zero evidence: stays text.
	if got := types["empty_dates"]; got != "VARCHAR" {
		t.Errorf("empty_dates type = %q, want VARCHAR", got)
	}

	// The phone column must survive exactly as written — leading zero included.
	if got := types["mobile_no"]; got != "VARCHAR" {
		t.Errorf("mobile_no type = %q, want VARCHAR", got)
	}
	rows, err := handle.Query(ctx, "SELECT mobile_no FROM read_parquet('"+target+"') ORDER BY amount")
	if err != nil {
		t.Fatalf("reading mobile_no: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scanning mobile_no: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating mobile_no: %v", err)
	}
	want := []string{"8034720413", "0401234567", "9911122233"}
	if !slicesEqual(got, want) {
		t.Errorf("mobile_no values = %v, want %v", got, want)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
