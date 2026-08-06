package ingest

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// This file covers the fix for Excel date columns lost to the text-only fallback. A real user file
// stored dates as Excel serials (43341) in cells whose style was the date format d-mmm-yy; the typed
// read_xlsx read them as real DATEs, but a mixed-type MOBILE_NO column (one text "002-2318820" in an
// otherwise numeric column) made the whole typed read fail, and the all-text fallback then read the
// date serials as strings and typed them as whole numbers. The hybrid fallback (see normalizeExcel's
// doc comment) keeps the typed read's DATE columns while still preserving the mixed-type cells from
// the text read.

// writeHybridXLSX builds an .xlsx whose second column is date-formatted (cell style 1 = numFmtId 15,
// "d-mmm-yy") holding Excel serials, and whose fourth column forces the typed read to fail by mixing a
// text "002-2318820" into an otherwise numeric column. The styles.xml is required, because the whole
// point is that the file itself declares the Date cells as dates.
func writeHybridXLSX(t *testing.T, path string) {
	t.Helper()

	var rows strings.Builder
	rows.WriteString(`<row r="1">` +
		`<c r="A1" t="inlineStr"><is><t>id</t></is></c>` +
		`<c r="B1" t="inlineStr"><is><t>date</t></is></c>` +
		`<c r="C1" t="inlineStr"><is><t>amount</t></is></c>` +
		`<c r="D1" t="inlineStr"><is><t>account</t></is></c></row>`)
	// Rows 2-4: date serials 43341 (2018-08-29), 43405 (2018-11-01), 43312 (2018-07-31).
	serials := []int{43341, 43405, 43312}
	for i, serial := range serials {
		r := i + 2
		acct := fmt.Sprintf(`<c r="D%d"><v>%d</v></c>`, r, 5000+r)
		if r == 3 {
			// One contradicting cell, exactly like the user file's MOBILE_NO column: forces the typed
			// read to give up and hand the whole file to the fallback.
			acct = fmt.Sprintf(`<c r="D%d" t="inlineStr"><is><t>002-2318820</t></is></c>`, r)
		}
		rows.WriteString(fmt.Sprintf(
			`<row r="%d"><c r="A%d"><v>%d</v></c>`+
				`<c r="B%d" s="1"><v>%d</v></c>`+
				`<c r="C%d"><v>1.5</v></c>%s</row>`,
			r, r, r, r, serial, r, acct))
	}

	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
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
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`</Relationships>`,
		"xl/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>` +
			`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
			`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
			`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
			`<cellXfs count="2">` +
			`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
			`<xf numFmtId="15" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
			`</cellXfs>` +
			`</styleSheet>`,
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

// TestNormalizeExcelHybridRead keeps the file's declared date column as DATE while preserving the
// mixed-type cell from the text read. This is the fix for the user's "date profiles as whole number":
// the date column must not become BIGINT just because some other column forced the text fallback.
func TestNormalizeExcelHybridRead(t *testing.T) {
	handle := acquireTestHandle(t)
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "hybrid.xlsx")
	target := filepath.Join(dir, "out.parquet")

	writeHybridXLSX(t, source)

	if err := normalizeToParquet(ctx, handle, domain.FormatExcel, source, target); err != nil {
		t.Fatalf("normalizeToParquet(Excel) error = %v", err)
	}

	// The date column survives the fallback as a real date, not the serial-as-whole-number it degraded
	// to before the hybrid fix.
	types := parquetColumnTypes(t, handle, target)
	if got := types["date"]; got != "DATE" {
		t.Errorf("date normalized type = %q, want DATE", got)
	}
	if got := types["id"]; got != "BIGINT" {
		t.Errorf("id normalized type = %q, want BIGINT (clean integer column keeps its narrowing)", got)
	}
	if got := types["account"]; got != "VARCHAR" {
		t.Errorf("account normalized type = %q, want VARCHAR (mixed column keeps all values as text)", got)
	}

	// The mixed-type account column preserved its text value verbatim (a date-serial reader must not
	// also start dropping the cells that forced the fallback).
	rows, err := handle.Query(ctx, "SELECT strftime(date, '%Y-%m-%d'), account FROM read_parquet('"+target+"') ORDER BY id")
	if err != nil {
		t.Fatalf("reading parquet: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var d, acct string
		if err := rows.Scan(&d, &acct); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, d+"|"+acct)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	want := []string{
		"2018-08-29|5002",
		"2018-11-01|002-2318820",
		"2018-07-31|5004",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("(date|account) rows = %v, want %v", got, want)
	}
}
