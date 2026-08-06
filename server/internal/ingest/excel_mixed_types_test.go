package ingest

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file covers the mixed-type Excel column: a real user upload failed with
//
//	could not read this Excel file: ... read_xlsx: Failed to parse cell 'E105':
//	Could not convert string '002-2318820' to DOUBLE
//
// read_xlsx types a column from the cells it meets first, then hard-fails on the first cell that
// contradicts it — very common in spreadsheets kept by hand. See normalizeExcel's doc comment for
// why the fix is a fallback rather than a flag.
//
// writeMixedTypeXLSX builds a minimal .xlsx by hand rather than reusing loaders_test.go's
// spatial/GDAL fixture writer, because GDAL types a whole column and this bug needs genuinely MIXED
// cell types within one column: numeric for the first hundred rows, then an inline string.
func writeMixedTypeXLSX(t *testing.T, path string, stringRow int, totalRows int) {
	t.Helper()

	var rows strings.Builder
	rows.WriteString(`<row r="1"><c r="A1" t="inlineStr"><is><t>id</t></is></c><c r="B1" t="inlineStr"><is><t>account</t></is></c></row>`)
	for i := 2; i <= totalRows; i++ {
		if i == stringRow {
			// A leading-zero account number: a string in the file, and one that MUST stay a string.
			rows.WriteString(fmt.Sprintf(
				`<row r="%d"><c r="A%d"><v>%d</v></c><c r="B%d" t="inlineStr"><is><t>002-2318820</t></is></c></row>`,
				i, i, i, i))
			continue
		}
		rows.WriteString(fmt.Sprintf(
			`<row r="%d"><c r="A%d"><v>%d</v></c><c r="B%d"><v>%d</v></c></row>`,
			i, i, i, i, 1000+i))
	}
	writeSheetXLSX(t, path, rows.String())
}

// writeLeadingZeroXLSX builds a sheet whose `code` column is text that LOOKS numeric — "002", "003"
// — alongside a mixed `account` column that forces the fallback path. The point is that entering the
// fallback must not license rewriting values elsewhere in the file.
func writeLeadingZeroXLSX(t *testing.T, path string, totalRows int) {
	t.Helper()

	var rows strings.Builder
	rows.WriteString(`<row r="1">` +
		`<c r="A1" t="inlineStr"><is><t>id</t></is></c>` +
		`<c r="B1" t="inlineStr"><is><t>code</t></is></c>` +
		`<c r="C1" t="inlineStr"><is><t>amount</t></is></c>` +
		`<c r="D1" t="inlineStr"><is><t>account</t></is></c></row>`)
	for i := 2; i <= totalRows; i++ {
		account := fmt.Sprintf(`<c r="D%d"><v>%d</v></c>`, i, 5000+i)
		if i == totalRows {
			// One contradicting cell, enough to make read_xlsx give up and hand over to the fallback.
			account = fmt.Sprintf(`<c r="D%d" t="inlineStr"><is><t>002-2318820</t></is></c>`, i)
		}
		rows.WriteString(fmt.Sprintf(
			`<row r="%d"><c r="A%d"><v>%d</v></c>`+
				`<c r="B%d" t="inlineStr"><is><t>%03d</t></is></c>`+
				`<c r="C%d" t="inlineStr"><is><t>1.50</t></is></c>%s</row>`,
			i, i, i, i, i, i, account))
	}
	writeSheetXLSX(t, path, rows.String())
}

// writeSheetXLSX wraps sheet-row XML in the minimum set of parts that make a valid .xlsx package.
func writeSheetXLSX(t *testing.T, path string, rowsXML string) {
	t.Helper()

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
			`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
			rowsXML + `</sheetData></worksheet>`,
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating xlsx: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
}

func TestNormalizeExcelRecoversFromMixedTypeColumn(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "data.xlsx")
	target := filepath.Join(dir, "out.parquet")

	// Row 105 mirrors the cell named in the original report.
	writeMixedTypeXLSX(t, source, 105, 200)

	if _, err := handle.Exec(ctx, "INSTALL excel; LOAD excel;"); err != nil {
		t.Skipf("excel extension unavailable (needs network on first install): %v", err)
	}

	if err := normalizeToParquet(ctx, handle, "excel", source, target); err != nil {
		t.Fatalf("normalizeToParquet(Excel, mixed-type column) error = %v, want nil", err)
	}

	// Every row must survive. A fallback that silently dropped the offending row would pass a
	// "no error" assertion while losing data, which is the failure mode being guarded against.
	count, err := rowCount(ctx, handle, target)
	if err != nil {
		t.Fatalf("rowCount() error = %v", err)
	}
	if count != 199 {
		t.Errorf("normalized row count = %d, want 199", count)
	}

	// The offending value itself must be intact and NOT NULL. `ignore_errors=true` would make the
	// ingest succeed with this cell nulled, so asserting on the value is what distinguishes the real
	// fix from the tempting one.
	rows, err := handle.Query(ctx, fmt.Sprintf(
		"SELECT account FROM read_parquet(%s) WHERE id = 105", sqlQuote(target)))
	if err != nil {
		t.Fatalf("querying the mixed-type cell: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row 105 is missing from the normalized output")
	}
	var account string
	if err := rows.Scan(&account); err != nil {
		t.Fatalf("scanning account: %v (a NULL here means the cell was silently discarded)", err)
	}
	if account != "002-2318820" {
		t.Errorf("account at row 105 = %q, want %q", account, "002-2318820")
	}
}

func TestNormalizeExcelKeepsRealTypesForWellBehavedColumns(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "data.xlsx")
	target := filepath.Join(dir, "out.parquet")
	writeMixedTypeXLSX(t, source, 105, 200)

	if _, err := handle.Exec(ctx, "INSTALL excel; LOAD excel;"); err != nil {
		t.Skipf("excel extension unavailable: %v", err)
	}
	if err := normalizeToParquet(ctx, handle, "excel", source, target); err != nil {
		t.Fatalf("normalizeToParquet() error = %v", err)
	}

	// The fallback must not flatten the whole file to text: only the genuinely mixed column becomes
	// VARCHAR. If this regresses, every Excel upload arrives as all-text and FEAT-005's profiling and
	// FEAT-006's type review become busywork for the user.
	rows, err := handle.Query(ctx, fmt.Sprintf(
		"SELECT typeof(id), typeof(account) FROM read_parquet(%s) LIMIT 1", sqlQuote(target)))
	if err != nil {
		t.Fatalf("typeof query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows in normalized output")
	}
	var idType, accountType string
	if err := rows.Scan(&idType, &accountType); err != nil {
		t.Fatalf("scanning types: %v", err)
	}
	if idType != "BIGINT" {
		t.Errorf("typeof(id) = %s, want BIGINT — the clean numeric column lost its type", idType)
	}
	if accountType != "VARCHAR" {
		t.Errorf("typeof(account) = %s, want VARCHAR", accountType)
	}
}

func TestNormalizeExcelFallbackNeverRewritesAValue(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "codes.xlsx")
	target := filepath.Join(dir, "out.parquet")
	writeLeadingZeroXLSX(t, source, 120)

	if _, err := handle.Exec(ctx, "INSTALL excel; LOAD excel;"); err != nil {
		t.Skipf("excel extension unavailable: %v", err)
	}
	if err := normalizeToParquet(ctx, handle, "excel", source, target); err != nil {
		t.Fatalf("normalizeToParquet() error = %v, want nil", err)
	}

	// Entering the fallback must not license rewriting values elsewhere in the file. "002" cast to an
	// integer and rendered back is "2", and "1.50" is "1.5" — both are values a person deliberately
	// stored as text in a spreadsheet (an account code, a price), and both would be silently altered
	// by any type inference that checks only whether a cast SUCCEEDS rather than whether it ROUND-TRIPS.
	rows, err := handle.Query(ctx, fmt.Sprintf(
		"SELECT code, amount FROM read_parquet(%s) WHERE id = 2", sqlQuote(target)))
	if err != nil {
		t.Fatalf("querying preserved values: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row id=2 missing from normalized output")
	}
	var code, amount string
	if err := rows.Scan(&code, &amount); err != nil {
		t.Fatalf("scanning: %v (a type error here means the column was numeric-typed and rewritten)", err)
	}
	if code != "002" {
		t.Errorf("code = %q, want %q — leading zeros were destroyed", code, "002")
	}
	if amount != "1.50" {
		t.Errorf("amount = %q, want %q — trailing zero was destroyed", amount, "1.50")
	}
}

func TestExcelColumnTypeRejectsTypeThatWouldRewriteValues(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "codes.xlsx")
	writeLeadingZeroXLSX(t, source, 120)
	if _, err := handle.Exec(ctx, "INSTALL excel; LOAD excel;"); err != nil {
		t.Skipf("excel extension unavailable: %v", err)
	}
	textRead := fmt.Sprintf("read_xlsx(%s, all_varchar=true)", sqlQuote(source))

	// Guards the inner coalesce in excelColumnType: TRY_CAST yields NULL for an uncastable value, the
	// comparison then yields NULL, and bool_and SKIPS NULLs — so without it, a column of clean numbers
	// plus one "002-2318820" reports as BIGINT and the odd value is destroyed by the following CAST.
	// This was a real bug in the first version of this function, caught by a test rather than review.
	for _, tc := range []struct{ column, want string }{
		{"id", "BIGINT"},       // genuinely numeric, safe to type
		{"code", "VARCHAR"},    // "002" would render back as "2"
		{"amount", "VARCHAR"},  // "1.50" would render back as "1.5"
		{"account", "VARCHAR"}, // one value cannot be cast at all
	} {
		got, err := excelColumnType(ctx, handle, textRead, tc.column)
		if err != nil {
			t.Fatalf("excelColumnType(%s) error = %v", tc.column, err)
		}
		if got != tc.want {
			t.Errorf("excelColumnType(%s) = %s, want %s", tc.column, got, tc.want)
		}
	}
}
