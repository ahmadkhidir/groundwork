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
		return copyToParquet(ctx, h,
			fmt.Sprintf("SELECT * FROM read_csv(%s, auto_detect=true, sample_size=-1)", sqlQuote(sourcePath)),
			parquetPath, "CSV")

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
// `ignore_errors=true` is NOT the fix, though it makes the error disappear: it nulls the offending
// cell. Verified against the reproduction above — the account number becomes NULL and the column
// stays DOUBLE, so the user silently loses exactly the value that made their data interesting. Silent
// wrongness is the one failure mode this product exists to prevent (see README's differentiator);
// a loud failure is strictly better, and a correct read is better still.
//
// So: try the typed read first, and fall back only when it fails. The fallback re-reads every cell as
// text (all_varchar, which cannot fail on a type conflict because it makes no type decision) and then
// types each column itself, per excelColumnType below.
//
// Ordering matters and is deliberate: the typed read stays the default so a well-formed spreadsheet's
// dates and numbers keep the types read_xlsx derives from its cell formats. The fallback only ever
// runs where the current behaviour is a hard failure, so it cannot regress a file that works today.
func normalizeExcel(ctx context.Context, h execer, sourcePath, parquetPath string) error {
	typedErr := copyToParquet(ctx, h,
		fmt.Sprintf("SELECT * FROM read_xlsx(%s)", sqlQuote(sourcePath)),
		parquetPath, "Excel")
	if typedErr == nil {
		return nil
	}

	textRead := fmt.Sprintf("read_xlsx(%s, all_varchar=true)", sqlQuote(sourcePath))

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
