package ingest

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// execer is the subset of engine.Handle the loaders need. Defined narrowly so loaders.go's tests
// don't have to build a full Handle.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (engine.Rows, error)
}

// normalizeToParquet loads sourcePath (the originally-uploaded file, already downloaded to local
// disk) through the workspace's engine and writes the canonical normalized form to parquetPath, both
// local filesystem paths. Every returned error is already a specific, user-facing message — FEAT-004's
// acceptance criterion "ingest failure surfaces a specific, actionable error" — never a bare wrapped
// driver error.
//
// Paths are embedded as quoted SQL string literals rather than passed as `?` bind parameters:
// DuckDB's table functions (read_csv, read_parquet, read_xlsx) and COPY ... TO need their path argument
// at bind/plan time, before a driver-level parameter would be substituted, and testing against this
// driver confirmed COPY's target path specifically does not accept a bound parameter (it is parsed,
// not bound). Every path here is server-generated (a local temp file this package created), never
// user input, so literal embedding carries no injection risk — sqlQuote still escapes it defensively.
//
// CSV and Parquet need no DuckDB extension at all. Excel and SQLite need exactly two, and they are
// named here to match architecture.md and risk_log.md R-017 exactly: `excel` (via read_xlsx) and
// `sqlite_scanner`. Those are the two a production container image must preinstall.
//
// Do not substitute `spatial` for Excel, which an earlier revision of this file did via st_read.
// It works — spatial reads xlsx through GDAL — but it silently defeats R-017's mitigation: an image
// that preinstalled the documented `excel` extension would still attempt a runtime fetch of
// `spatial` from extensions.duckdb.org, and the first Excel upload on a locked-down production
// network would fail in exactly the way R-017 describes. It is also far heavier, pulling in all of
// GDAL to read a spreadsheet.
func normalizeToParquet(ctx context.Context, h execer, format domain.DatasetFormat, sourcePath, parquetPath string) error {
	switch format {
	case domain.FormatCSV:
		return normalizeCSV(ctx, h, sourcePath, parquetPath)

	case domain.FormatParquet:
		return copyToParquet(ctx, h,
			fmt.Sprintf("SELECT * FROM read_parquet(%s)", sqlQuote(sourcePath)),
			parquetPath, "Parquet")

	case domain.FormatExcel:
		if _, err := h.Exec(ctx, "INSTALL excel; LOAD excel;"); err != nil {
			return fmt.Errorf("could not load Excel support for this engine: %s", cleanDriverError(err))
		}
		return normalizeExcel(ctx, h, sourcePath, parquetPath)

	case domain.FormatSQLite:
		return normalizeSQLite(ctx, h, sourcePath, parquetPath)

	default:
		return fmt.Errorf("%q is not a supported dataset format", format)
	}
}

// copyToParquet runs `COPY (<selectSQL>) TO '<parquetPath>' (FORMAT parquet)`.
func copyToParquet(ctx context.Context, h execer, selectSQL, parquetPath, formatLabel string) error {
	statement := fmt.Sprintf("COPY (%s) TO %s (FORMAT parquet)", selectSQL, sqlQuote(parquetPath))
	if _, err := h.Exec(ctx, statement); err != nil {
		return fmt.Errorf("could not read this %s file: %s", formatLabel, cleanDriverError(err))
	}
	return nil
}

// csvDateFormats are the strptime formats the CSV normalizer recognizes as dates that DuckDB's
// sniffer misses. The sniffer reads ISO and numeric date formats itself, so this list exists only for
// the formats it does not know at all — Excel's dd-mmm-yy exports like "29-Aug-18". %b matches
// abbreviated month names case-insensitively; %y is the two-digit Excel-export year (00-68 maps to
// 2000s, 69-99 to 1900s, per strptime), %Y the four-digit form. strptime returns TIMESTAMP, so
// callers CAST the result to DATE. Formats other than these (e.g. "29 Aug 18") are out of scope:
// they stay VARCHAR and surface in the type-review step as low-confidence text, which is exactly what
// the review step exists for.
var csvDateFormats = []string{"%d-%b-%y", "%d-%b-%Y"}

// strptimeDateExpr builds a COALESCE over every recognized date format, so a single column mixing
// two- and four-digit years (common in hand-maintained CSVs) still converts completely.
func strptimeDateExpr(ident string) string {
	exprs := make([]string, 0, len(csvDateFormats))
	for _, format := range csvDateFormats {
		exprs = append(exprs, fmt.Sprintf("TRY_STRPTIME(%s, '%s')", ident, format))
	}
	return "COALESCE(" + strings.Join(exprs, ", ") + ")"
}

// csvColumn is one column of a CSV as the sniffer sees it.
type csvColumn struct {
	name     string
	duckType string
}

// normalizeCSV ingests a .csv file via DuckDB's sniffer (read_csv with auto-detect over the whole
// file), then repairs one specific sniffer blind spot: columns of dd-mmm-yy dates, which the sniffer
// reads as VARCHAR because it does not recognize that format at all.
//
// A column of "29-Aug-18" values previously stayed text end to end: inferred type string, date fit
// rate 0 (floored to the 40% confidence badge), and date math failing at query time — all because
// nothing in the pipeline could parse the format. The sniffer's own DATE inference handles ISO and
// numeric formats, so this only looks at columns that came out VARCHAR.
//
// The conversion is deliberately conservative, matching normalizeExcel's philosophy: a VARCHAR column
// becomes DATE only if EVERY non-null value parses with strptime. Any value that does not parse keeps
// the whole column VARCHAR — never silently nulled — so the review step still sees the ambiguity
// (a low text fit rate) rather than quietly losing data.
func normalizeCSV(ctx context.Context, h execer, sourcePath, parquetPath string) error {
	readExpr := fmt.Sprintf("read_csv(%s, auto_detect=true, sample_size=-1)", sqlQuote(sourcePath))

	columns, err := csvColumns(ctx, h, readExpr)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return fmt.Errorf("this CSV file has no columns to ingest")
	}

	// One combined detection query (one extra scan of the file) rather than one query per text
	// column. Columns the sniffer already typed correctly are never touched, and a CSV with no
	// VARCHAR columns (the common case: all numeric or ISO dates) costs nothing beyond the DESCRIBE.
	convertible := make([]bool, len(columns))
	var textIdx []int
	for i, col := range columns {
		if strings.EqualFold(col.duckType, "VARCHAR") {
			textIdx = append(textIdx, i)
		}
	}
	if len(textIdx) > 0 {
		var detection []string
		for _, i := range textIdx {
			ident := quoteIdent(columns[i].name)
			detection = append(detection,
				fmt.Sprintf("count(*) FILTER (WHERE %[1]s IS NOT NULL AND %[2]s IS NOT NULL) AS __csv_parse_%[3]d__", ident, strptimeDateExpr(ident), i),
				fmt.Sprintf("count(*) FILTER (WHERE %[1]s IS NOT NULL) AS __csv_nonnull_%[2]d__", ident, i),
			)
		}
		rows, err := h.Query(ctx, fmt.Sprintf("SELECT %s FROM %s", strings.Join(detection, ", "), readExpr))
		if err != nil {
			return fmt.Errorf("could not inspect this CSV file: %s", cleanDriverError(err))
		}
		// count(*) FILTER aggregates are scanned into *int64 per text column; a NULL scan
		// destination (no data rows at all) keeps the column text.
		type dateCount struct {
			index   int
			parse   int64
			nonNull int64
		}
		counts := make([]*dateCount, 0, len(textIdx))
		dests := make([]any, 0, len(textIdx)*2)
		for _, i := range textIdx {
			c := &dateCount{index: i}
			counts = append(counts, c)
			dests = append(dests, &c.parse, &c.nonNull)
		}
		if !rows.Next() {
			rows.Close()
			return fmt.Errorf("could not inspect this CSV file: the schema scan returned no rows")
		}
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return fmt.Errorf("could not inspect this CSV file: %s", cleanDriverError(err))
		}
		rows.Close()
		for _, c := range counts {
			// A column converts only when it has at least one value and EVERY non-null value
			// parsed. nonNull == 0 (all-empty column) stays text — converting on zero evidence
			// would be a type decision nothing supported.
			if c.nonNull > 0 && c.parse == c.nonNull {
				convertible[c.index] = true
			}
		}
	}

	// Only columns that parsed as dates on every non-null value get cast; the rest pass through
	// exactly as the sniffer read them.
	projections := make([]string, 0, len(columns))
	for i, col := range columns {
		ident := quoteIdent(col.name)
		if convertible[i] {
			projections = append(projections, fmt.Sprintf("CAST(%s AS DATE) AS %s", strptimeDateExpr(ident), ident))
		} else {
			projections = append(projections, ident)
		}
	}
	return copyToParquet(ctx, h,
		fmt.Sprintf("SELECT %s FROM %s", strings.Join(projections, ", "), readExpr),
		parquetPath, "CSV")
}

// csvColumns lists column names and DuckDB types from the sniffer's read of a CSV.
func csvColumns(ctx context.Context, h execer, readExpr string) ([]csvColumn, error) {
	rows, err := h.Query(ctx, fmt.Sprintf("DESCRIBE SELECT * FROM %s", readExpr))
	if err != nil {
		return nil, fmt.Errorf("could not inspect this CSV file: %s", cleanDriverError(err))
	}
	defer rows.Close()

	// DESCRIBE reports six columns (column_name, column_type, null, key, default, extra); read them
	// all rather than assuming a shape, so a DuckDB minor version cannot silently shift the scan.
	numCols := len(rows.Columns())
	if numCols == 0 {
		numCols = 6
	}
	var out []csvColumn
	for rows.Next() {
		dest := make([]any, numCols)
		ptrs := make([]any, numCols)
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("could not inspect this CSV file: %w", err)
		}
		out = append(out, csvColumn{name: cellString(dest[0]), duckType: cellString(dest[1])})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("could not inspect this CSV file: %w", err)
	}
	return out, nil
}

// cellString renders a scan destination cell as a string, handling the string/[]byte shapes DuckDB
// drivers return.
func cellString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}

// normalizeExcel ingests an .xlsx file, falling back to whole-file type inference when DuckDB's
// per-cell typing gives up.
//
// read_xlsx types a column from the cell types it meets first, then hard-fails on the first cell that
// contradicts it:
//
//	Invalid Input Error: read_xlsx: Failed to parse cell 'E105':
//	Could not convert string '002-2318820' to DOUBLE
//
// That is extremely common in real spreadsheets — an ID or account column that is numeric for a
// hundred rows and then hits a value with a dash or a leading zero. Unlike read_csv, read_xlsx has no
// sample_size parameter (its full option list is col0, header, all_varchar, stop_at_empty,
// ignore_errors, range, sheet, empty_as_varchar, normalize_names), so the detection window cannot
// simply be widened.
//
// The file's own cell formats are the metadata that matters, and they must survive the fallback. An
// .xlsx date column stores Excel serials (43341) but marks every cell with a date format (d-mmm-yy);
// read_xlsx honours that and returns a real DATE column — as long as the typed read runs. The all-text
// fallback cannot see the format, reads "43341" as a string, and types the column as a whole number:
// the date becomes an integer end to end with no mechanism to know better. That is the failure this
// function exists to prevent, so the fallback is built to keep the typed read's metadata columns.
//
// Fallback plan, in order:
//
//  1. The plain typed read stays the default (a well-formed spreadsheet keeps all of read_xlsx's
//     types, dates included) — it only runs the fallback when it hard-fails.
//  2. Hybrid read: retry the typed read with `ignore_errors=true`. That read succeeds where the plain
//     one failed (it NULLs exactly the offending cells) and still applies the file's cell formats, so
//     date columns come back as real DATEs. Then drop the DATE/TIMESTAMP/BOOLEAN columns whose typed
//     read dropped no values straight into the Parquet, and build every other column from the all-text
//     read through the existing excelColumnType round-trip — the mixed-type cell that caused the
//     failure is one of those, so its text value is preserved rather than nulled. The two reads are
//     joined by row number; both scan the same file in the same order, and the pairings are verified
//     by TestNormalizeExcelHybridRead against a real fixture.
//  3. Text-only fallback: prior behaviour. Only used when the hybrid read itself cannot run (file
//     unreadable at the sheet level, or a date column whose typed read dropped values).
func normalizeExcel(ctx context.Context, h execer, sourcePath, parquetPath string) error {
	typedErr := copyToParquet(ctx, h,
		fmt.Sprintf("SELECT * FROM read_xlsx(%s)", sqlQuote(sourcePath)),
		parquetPath, "Excel")
	if typedErr == nil {
		return nil
	}

	typedRead := fmt.Sprintf("read_xlsx(%s, ignore_errors=true)", sqlQuote(sourcePath))
	textRead := fmt.Sprintf("read_xlsx(%s, all_varchar=true)", sqlQuote(sourcePath))

	// Prefer the hybrid read; it is the only fallback that preserves the file's date metadata. If its
	// typed half fails to describe (sheet-level corruption, not a per-cell coercion), or if it finds
	// nothing worth keeping, fall through to the text-only path rather than failing — the file worked
	// before the hybrid existed, and it must keep working.
	typedCols, err := excelColumnTypes(ctx, h, typedRead)
	if err == nil {
		if hybridErr := excelHybrid(ctx, h, typedRead, textRead, typedCols, parquetPath); hybridErr == nil {
			return nil
		}
	}

	names, err := excelColumnNames(ctx, h, textRead)
	if err != nil {
		// The all-text read failed too, so the file is unreadable for a reason the fallback cannot
		// address. Report the ORIGINAL typed-read error: it names the offending cell, which is what
		// makes the message actionable per FEAT-004.
		return typedErr
	}

	projections := make([]string, 0, len(names))
	for _, name := range names {
		sqlType, err := excelColumnType(ctx, h, textRead, name)
		if err != nil {
			return typedErr
		}
		ident := quoteIdent(name)
		if sqlType == "VARCHAR" {
			projections = append(projections, ident)
			continue
		}
		projections = append(projections, fmt.Sprintf("CAST(%s AS %s) AS %s", ident, sqlType, ident))
	}

	if err := copyToParquet(ctx, h,
		fmt.Sprintf("SELECT %s FROM %s", strings.Join(projections, ", "), textRead),
		parquetPath, "Excel",
	); err != nil {
		return typedErr
	}
	return nil
}

// excelKeepTyped reports whether a DuckDB type from the ignore_errors read is one of the kinds only
// the file's cell-format metadata produces reliably — the columns the hybrid read sources from the
// typed read. Numbers are deliberately excluded: read_xlsx reports every numeric column as DOUBLE
// even when the values are whole integers, and narrowing those to BIGINT is the existing
// excelColumnType round-trip's job, unchanged.
func excelKeepTyped(duckType string) bool {
	upper := strings.ToUpper(duckType)
	return strings.HasPrefix(upper, "DATE") ||
		strings.HasPrefix(upper, "TIMESTAMP") ||
		strings.HasPrefix(upper, "BOOLEAN")
}

// excelColumnTypes lists column names and DuckDB types from the ignore_errors read, which is what
// tells the hybrid path which columns the file itself declares as dates (via cell formats).
func excelColumnTypes(ctx context.Context, h execer, typedRead string) ([]csvColumn, error) {
	rows, err := h.Query(ctx, fmt.Sprintf("DESCRIBE SELECT * FROM %s", typedRead))
	if err != nil {
		return nil, fmt.Errorf("describing Excel columns: %w", err)
	}
	defer rows.Close()

	numCols := len(rows.Columns())
	if numCols == 0 {
		numCols = 6 // DuckDB's DESCRIBE shape: column_name, column_type, null, key, default, extra
	}
	var out []csvColumn
	for rows.Next() {
		dest := make([]any, numCols)
		ptrs := make([]any, numCols)
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scanning Excel column schema: %w", err)
		}
		out = append(out, csvColumn{name: cellString(dest[0]), duckType: cellString(dest[1])})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading Excel column schema: %w", err)
	}
	return out, nil
}

// excelHybrid builds the normalized Parquet from the typed (ignore_errors) read and the all-text
// read joined by row number, taking each column from whichever read is honest for it:
//
//   - DATE/TIMESTAMP/BOOLEAN columns that the typed read fully preserved come from the typed read,
//     so Excel serial dates land as real dates.
//   - Everything else comes from the text read through excelColumnType's round-trip, exactly as the
//     text-only fallback does — including the mixed-type columns whose cells the typed read dropped,
//     so those values survive verbatim.
//
// It returns an error (and lets normalizeExcel fall back to the text-only path) when there is no
// column worth keeping from the typed read — the hybrid's extra complexity buys nothing for a file
// with no date-typed columns.
func excelHybrid(ctx context.Context, h execer, typedRead, textRead string, typedCols []csvColumn, parquetPath string) error {
	// Resolve the keep-typed candidates against the text read's column list. Both reads describe the
	// same sheet, so the name order is identical; the map lets the projection loop consult by name.
	textNames, err := excelColumnNames(ctx, h, textRead)
	if err != nil {
		return err
	}
	typeByName := make(map[string]string, len(typedCols))
	for _, c := range typedCols {
		typeByName[c.name] = c.duckType
	}

	var candidates []string
	for _, name := range textNames {
		if duckType, ok := typeByName[name]; ok && excelKeepTyped(duckType) {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no columns whose cell-format metadata the typed read preserves")
	}

	// A keep-typed column is usable only if the typed read dropped none of its values — otherwise its
	// NULLs would silently destroy data. Dropped cells are the whole reason the plain typed read
	// failed, and they land in exactly the mixed-type columns; a DATE column losing a value is rare
	// but possible, and the text read must win when it happens.
	dropped, err := excelDroppedCounts(ctx, h, typedRead, textRead, candidates)
	if err != nil {
		return err
	}
	sourceTyped := make(map[string]bool, len(candidates))
	anyTyped := false
	for _, name := range candidates {
		sourceTyped[name] = dropped[name] == 0
		anyTyped = anyTyped || sourceTyped[name]
	}
	if !anyTyped {
		return fmt.Errorf("every metadata column lost values in the typed read; nothing safe to keep")
	}

	projections := make([]string, 0, len(textNames))
	for _, name := range textNames {
		ident := quoteIdent(name)
		if sourceTyped[name] {
			projections = append(projections, fmt.Sprintf("t.%s", ident))
			continue
		}
		sqlType, err := excelColumnType(ctx, h, textRead, name)
		if err != nil {
			return err
		}
		if sqlType == "VARCHAR" {
			projections = append(projections, fmt.Sprintf("s.%s", ident))
			continue
		}
		projections = append(projections, fmt.Sprintf("CAST(s.%s AS %s) AS %s", ident, sqlType, ident))
	}

	query := fmt.Sprintf(
		`WITH t AS (SELECT row_number() OVER () AS __rn__, * FROM %[1]s),
		        s AS (SELECT row_number() OVER () AS __rn__, * FROM %[2]s)
		 SELECT %[3]s FROM t JOIN s USING (__rn__)`,
		typedRead, textRead, strings.Join(projections, ", "),
	)
	return copyToParquet(ctx, h, query, parquetPath, "Excel")
}

// excelDroppedCounts reports, for each named column, how many rows the typed read has NULL where the
// text read has a value — the cells ignore_errors silently dropped. Both reads are the same sheet
// scanned in the same order, so row_number() OVER () aligns them; this pairing is exercised by
// TestNormalizeExcelHybridRead against a real fixture.
func excelDroppedCounts(ctx context.Context, h execer, typedRead, textRead string, names []string) (map[string]int64, error) {
	filters := make([]string, 0, len(names))
	for _, name := range names {
		ident := quoteIdent(name)
		filters = append(filters, fmt.Sprintf("count(*) FILTER (WHERE t.%s IS NULL AND s.%s IS NOT NULL)", ident, ident))
	}
	query := fmt.Sprintf(
		`WITH t AS (SELECT row_number() OVER () AS __rn__, * FROM %[1]s),
		        s AS (SELECT row_number() OVER () AS __rn__, * FROM %[2]s)
		 SELECT %[3]s FROM t JOIN s USING (__rn__)`,
		typedRead, textRead, strings.Join(filters, ", "),
	)

	rows, err := h.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("counting dropped Excel cells: %s", cleanDriverError(err))
	}
	defer rows.Close()

	out := make(map[string]int64, len(names))
	if !rows.Next() {
		return nil, fmt.Errorf("counting dropped Excel cells: the scan returned no rows")
	}
	// count(*) FILTER always yields a non-null BIGINT (0 or more), so scanning straight into int64
	// is safe; the only NULL case is an empty sheet, which rows.Next() above already ruled out.
	dests := make([]any, len(names))
	ptrs := make([]any, len(names))
	vals := make([]int64, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	dests = ptrs
	if err := rows.Scan(dests...); err != nil {
		return nil, fmt.Errorf("counting dropped Excel cells: %w", err)
	}
	for i, name := range names {
		out[name] = vals[i]
	}
	return out, nil
}

// excelColumnNames lists the column names produced by the all-text read, in order.
func excelColumnNames(ctx context.Context, h execer, textRead string) ([]string, error) {
	rows, err := h.Query(ctx, fmt.Sprintf("SELECT column_name FROM (DESCRIBE SELECT * FROM %s)", textRead))
	if err != nil {
		return nil, fmt.Errorf("describing Excel columns: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning Excel column name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating Excel columns: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("Excel file produced no columns")
	}
	return names, nil
}

// excelColumnType chooses a SQL type for one all-text column, and it is deliberately conservative:
// a type is adopted ONLY if casting to it and back reproduces the original text of EVERY non-null
// value, byte for byte. Otherwise the column stays VARCHAR.
//
// The round-trip is the whole point, not a belt-and-braces extra. Casting alone would happily accept
// values it also quietly rewrites — an account number "002" casts to BIGINT perfectly well and comes
// back as "2", and "1.50" comes back as "1.5". Those are the columns most likely to be identifiers or
// money in a real spreadsheet, and a spreadsheet is precisely where people keep such values as text
// on purpose. Since the round-trip compares the rendered value against the original, any column whose
// text would not survive the trip is left alone as text rather than silently rewritten.
//
// This is why the fallback carries no "leading zeros may be lost" caveat: it cannot alter a value. The
// worst it can do is decline to type a column, which the user can still correct on the ingest
// confirmation screen (FEAT-006), and which is visible rather than silent.
//
// Types are tried narrowest-first so a whole-number column does not land as DOUBLE. TIMESTAMP is not
// attempted: a date typed here would have to render back in exactly DuckDB's canonical format to pass
// the round-trip, so it would almost always fall through to VARCHAR anyway, and attempting it costs a
// query per column for no gain.
func excelColumnType(ctx context.Context, h execer, textRead, columnName string) (string, error) {
	ident := quoteIdent(columnName)

	for _, candidate := range []string{"BIGINT", "DOUBLE"} {
		// The INNER coalesce is load-bearing and easy to get wrong: TRY_CAST returns NULL for a value
		// that cannot be cast, so the comparison yields NULL rather than false — and bool_and SKIPS
		// NULLs. Without it, a column of clean numbers plus one "002-2318820" reports true, the column
		// is typed BIGINT, and the offending value is destroyed by the subsequent CAST. That is the
		// exact data loss this function exists to prevent, so a failed cast is coalesced to false.
		//
		// The OUTER coalesce handles an all-NULL or empty column, where bool_and itself returns NULL:
		// VARCHAR is the honest answer, since nothing observed justifies a narrower type.
		query := fmt.Sprintf(
			`SELECT coalesce(bool_and(coalesce(CAST(TRY_CAST(%[1]s AS %[2]s) AS VARCHAR) = %[1]s, false)), false)
			   FROM %[3]s WHERE %[1]s IS NOT NULL`,
			ident, candidate, textRead)

		rows, err := h.Query(ctx, query)
		if err != nil {
			return "", fmt.Errorf("testing %s as %s: %w", columnName, candidate, err)
		}
		var roundTrips bool
		if rows.Next() {
			if err := rows.Scan(&roundTrips); err != nil {
				rows.Close()
				return "", fmt.Errorf("scanning round-trip result for %s: %w", columnName, err)
			}
		}
		rows.Close()

		if roundTrips {
			return candidate, nil
		}
	}
	return "VARCHAR", nil
}

// normalizeSQLite ingests a single-table SQLite file. Multi-table files are rejected with a specific,
// actionable message rather than guessing which table the user meant — this release does not attempt
// to split a SQLite file into multiple Datasets.
func normalizeSQLite(ctx context.Context, h execer, sourcePath, parquetPath string) error {
	// sqlite_scanner is the canonical extension name; `INSTALL sqlite` is an alias that resolves to
	// it. The canonical name is used here so this line matches architecture.md and risk_log.md
	// R-017 literally — those documents name the two extensions a production image must preinstall,
	// and an alias in the code is one more step between reading the mitigation and checking it.
	if _, err := h.Exec(ctx, "INSTALL sqlite_scanner; LOAD sqlite_scanner;"); err != nil {
		return fmt.Errorf("could not load SQLite support for this engine: %s", cleanDriverError(err))
	}

	// ATTACH under a fixed, process-local schema alias. Safe to hardcode: each ingest run acquires
	// its own engine.Handle lease (see engine.Provider's contract), so no two ingest runs share a
	// connection concurrently.
	const schema = "ingest_src"
	if _, err := h.Exec(ctx, fmt.Sprintf("ATTACH %s AS %s (TYPE SQLITE)", sqlQuote(sourcePath), schema)); err != nil {
		return fmt.Errorf("could not open this SQLite file: %s", cleanDriverError(err))
	}
	defer func() { _, _ = h.Exec(ctx, fmt.Sprintf("DETACH %s", schema)) }()

	tables, err := sqliteTableNames(ctx, h, schema)
	if err != nil {
		return fmt.Errorf("could not list tables in this SQLite file: %s", cleanDriverError(err))
	}
	if len(tables) == 0 {
		return fmt.Errorf("this SQLite file has no tables to ingest")
	}
	if len(tables) > 1 {
		return fmt.Errorf(
			"this SQLite file has %d tables (%s); Groundwork ingests single-table SQLite files only in this release — export the table you need as CSV instead",
			len(tables), strings.Join(tables, ", "),
		)
	}

	selectSQL := fmt.Sprintf("SELECT * FROM %s.%s", schema, quoteIdent(tables[0]))
	return copyToParquet(ctx, h, selectSQL, parquetPath, "SQLite")
}

// sqliteTableNames lists user tables in the attached SQLite database.
//
// Uses DuckDB's own duckdb_tables() catalog function rather than SQLite's sqlite_master. The
// sqlite extension does NOT expose sqlite_master through the attached catalog — neither
// `<alias>.sqlite_master` nor `<alias>.main.sqlite_master` resolves, both failing with "Table with
// name sqlite_master does not exist". Verified against real single- and multi-table SQLite files;
// the original sqlite_master form failed 100% of the time, which made every SQLite ingest fail
// before it reached the copy. Do not "simplify" this back to sqlite_master.
func sqliteTableNames(ctx context.Context, h execer, schema string) ([]string, error) {
	rows, err := h.Query(ctx,
		"SELECT table_name FROM duckdb_tables() WHERE database_name = ? ORDER BY table_name",
		schema,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// quoteIdent double-quotes a SQL identifier, doubling any embedded quote — standard SQL identifier
// escaping. table names here come from the file's own sqlite_master, not end-user input, but this
// still avoids building unescaped SQL out of external data.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqlQuote single-quotes a SQL string literal, doubling any embedded quote — standard SQL string
// escaping. See normalizeToParquet's doc comment for why literals are used here instead of bind
// parameters.
func sqlQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

// rowCount returns the row count of the Parquet file at path.
func rowCount(ctx context.Context, h execer, path string) (int64, error) {
	rows, err := h.Query(ctx, fmt.Sprintf("SELECT COUNT(*) FROM read_parquet(%s)", sqlQuote(path)))
	if err != nil {
		return 0, fmt.Errorf("counting normalized rows: %s", cleanDriverError(err))
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, fmt.Errorf("counting normalized rows: no result returned")
	}
	var count int64
	if err := rows.Scan(&count); err != nil {
		return 0, fmt.Errorf("counting normalized rows: %w", err)
	}
	return count, rows.Err()
}

// cleanDriverError trims the go-duckdb driver's own error-type prefix (e.g. "Error: Binder Error:
// ...") down to its message, which is already specific about the offending row/column/encoding —
// exactly the acceptance criterion's "malformed CSV row, unsupported encoding" style of detail —
// without also leaking the Go error-wrapping chain to the end user.
func cleanDriverError(err error) string {
	msg := err.Error()
	// Collapse to a single line and cap length so one runaway parser message can't blow out the
	// Ingest Error state's layout.
	msg = strings.ReplaceAll(msg, "\n", " ")
	const maxLen = 400
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}

// formatByteCount renders n bytes as a short human label, used only in log lines here (the
// user-facing 500 MB ceiling message itself lives in config/upload.go, per rule 12 — this is not a
// second copy of that constant, just a log formatter).
func formatByteCount(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB"}
	div, exp := float64(1024), 0
	for f := float64(n) / 1024; f >= 1024 && exp < len(units)-1; f /= 1024 {
		div *= 1024
		exp++
	}
	return strconv.FormatFloat(float64(n)/div, 'f', 1, 64) + " " + units[exp]
}
