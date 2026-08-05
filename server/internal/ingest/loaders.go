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
// DuckDB's table functions (read_csv, read_parquet, st_read) and COPY ... TO need their path argument
// at bind/plan time, before a driver-level parameter would be substituted, and testing against this
// driver confirmed COPY's target path specifically does not accept a bound parameter (it is parsed,
// not bound). Every path here is server-generated (a local temp file this package created), never
// user input, so literal embedding carries no injection risk — sqlQuote still escapes it defensively.
//
// CSV and Parquet are implemented against capability verified end to end ahead of this wave (CSV type
// sniffing via read_csv, Parquet write via COPY ... FORMAT parquet — see IMPLEMENTATION.md §2).
// Excel and SQLite use DuckDB's standard `spatial` and `sqlite` extensions respectively; unlike the
// CSV/Parquet path, these two were NOT exercised against real files during this wave (no verified
// Excel/SQLite fixture was available, and extension installation needs network access at ingest
// time) — flagged in this wave's report as needing a follow-up verification pass.
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
		if _, err := h.Exec(ctx, "INSTALL spatial; LOAD spatial;"); err != nil {
			return fmt.Errorf("could not load Excel support for this engine: %s", cleanDriverError(err))
		}
		return copyToParquet(ctx, h,
			fmt.Sprintf("SELECT * FROM st_read(%s)", sqlQuote(sourcePath)),
			parquetPath, "Excel")

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

// normalizeSQLite ingests a single-table SQLite file. Multi-table files are rejected with a specific,
// actionable message rather than guessing which table the user meant — this release does not attempt
// to split a SQLite file into multiple Datasets.
func normalizeSQLite(ctx context.Context, h execer, sourcePath, parquetPath string) error {
	if _, err := h.Exec(ctx, "INSTALL sqlite; LOAD sqlite;"); err != nil {
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
