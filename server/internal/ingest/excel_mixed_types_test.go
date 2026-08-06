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
			rows.String() + `</sheetData></worksheet>`,
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
