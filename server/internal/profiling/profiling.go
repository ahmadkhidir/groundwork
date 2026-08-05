// Package profiling computes per-column statistics for a normalized dataset — distinct-value count,
// null rate, and inferred type — per FEAT-005 (Data Profiling). It is the ground truth every
// downstream ingest-confirmation step (type review, PII flagging, relationship inference) reads from.
//
// Profiling runs through the Engine Provider seam exclusively (an Execer, satisfied by
// engine.Handle): this package never opens a *sql.DB or knows the engine is DuckDB, per
// architecture.md's boundary rule.
package profiling

import (
	"context"
	"fmt"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// Execer is the subset of engine.Handle profiling needs. Defined narrowly, consumer-side, so tests
// don't need a full Handle.
type Execer interface {
	Query(ctx context.Context, sql string, args ...any) (engine.Rows, error)
}

// ColumnStat is one column's computed profile, ready to be turned into a domain.Column row once the
// caller assigns it an ID and a dataset/workspace id.
type ColumnStat struct {
	Name           string
	InferredType   domain.InferredType
	TypeConfidence float64
	NullRate       float64
	DistinctCount  int64
}

// confidence bases per inferred type category. DuckDB's CSV/Excel/SQLite readers already ran their
// own type sniffing when the file was normalized to Parquet (see ingest/loaders.go); this package
// reads that decision back out of the Parquet schema via DESCRIBE rather than re-sniffing values
// itself. What is NOT available from DuckDB is a confidence number for that decision, so this package
// assigns one heuristically: a boolean/date/timestamp column is a highly distinctive shape (little
// room for a wrong guess), a numeric column is very likely correct but occasionally an
// all-numeric-looking string column (e.g. zero-padded IDs) is legitimately a string, and a string
// column is DuckDB's fallback once every more specific type was tried and rejected — a real
// conclusion, not "unknown," but the least distinctive of the three. features.md's acceptance
// criteria require a confidence score without specifying its formula; this heuristic is documented
// here precisely so it is inspectable and adjustable, not scattered as a magic number.
var confidenceBaseByType = map[domain.InferredType]float64{
	domain.TypeBoolean:   0.97,
	domain.TypeDate:      0.97,
	domain.TypeTimestamp: 0.97,
	domain.TypeInteger:   0.92,
	domain.TypeNumber:    0.92,
	domain.TypeString:    0.85,
}

// Profile computes distinct-value count, null rate, and inferred type for every column in the
// normalized Parquet file at parquetPath, in one combined aggregate query plus one schema query.
//
// A single combined aggregate query (rather than one query per column) keeps profiling a wide
// dataset from costing N sequential round trips through the engine — this is the "profiling
// completes before the user asks their first question" acceptance criterion in practice.
func Profile(ctx context.Context, h Execer, parquetPath string) ([]ColumnStat, error) {
	schema, err := describeColumns(ctx, h, parquetPath)
	if err != nil {
		return nil, err
	}
	if len(schema) == 0 {
		return nil, fmt.Errorf("profiling: %q has no columns to profile", parquetPath)
	}

	totalRows, nullCounts, distinctCounts, err := aggregateStats(ctx, h, parquetPath, schema)
	if err != nil {
		return nil, err
	}

	stats := make([]ColumnStat, 0, len(schema))
	for _, col := range schema {
		var nullRate float64
		if totalRows > 0 {
			nullRate = float64(nullCounts[col.name]) / float64(totalRows)
		}

		base := confidenceBaseByType[col.inferredType]
		if base == 0 {
			base = 0.5
		}
		// Higher null rates weaken confidence in the inferred type: a mostly-empty column gave
		// DuckDB's sniffer little to go on. Floored so confidence is never reported as ~0.
		confidence := base - nullRate*0.2
		if confidence < 0.4 {
			confidence = 0.4
		}

		stats = append(stats, ColumnStat{
			Name:           col.name,
			InferredType:   col.inferredType,
			TypeConfidence: confidence,
			NullRate:       nullRate,
			DistinctCount:  distinctCounts[col.name],
		})
	}
	return stats, nil
}

type columnSchema struct {
	name         string
	inferredType domain.InferredType
}

// describeColumns reads the Parquet file's schema via DESCRIBE, which DuckDB answers from the file's
// metadata without scanning any rows.
func describeColumns(ctx context.Context, h Execer, parquetPath string) ([]columnSchema, error) {
	rows, err := h.Query(ctx, fmt.Sprintf("DESCRIBE SELECT * FROM read_parquet(%s)", sqlQuote(parquetPath)))
	if err != nil {
		return nil, fmt.Errorf("profiling: describing schema: %w", err)
	}
	defer rows.Close()

	numCols := len(rows.Columns())
	if numCols == 0 {
		numCols = 6 // DuckDB's DESCRIBE shape: column_name, column_type, null, key, default, extra
	}

	var out []columnSchema
	for rows.Next() {
		dest := make([]any, numCols)
		ptrs := make([]any, numCols)
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("profiling: scanning schema row: %w", err)
		}
		name := asString(dest[0])
		duckType := asString(dest[1])
		out = append(out, columnSchema{name: name, inferredType: mapDuckDBType(duckType)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("profiling: reading schema: %w", err)
	}
	return out, nil
}

// aggregateStats runs one combined query computing the total row count plus a null count and an
// approximate distinct count per column.
func aggregateStats(ctx context.Context, h Execer, parquetPath string, schema []columnSchema) (int64, map[string]int64, map[string]int64, error) {
	selectParts := []string{"count(*) AS __total_rows__"}
	nullAlias := make(map[string]string, len(schema))
	distinctAlias := make(map[string]string, len(schema))

	for i, col := range schema {
		ident := quoteIdent(col.name)
		nAlias := fmt.Sprintf("__null_%d__", i)
		dAlias := fmt.Sprintf("__distinct_%d__", i)
		nullAlias[col.name] = nAlias
		distinctAlias[col.name] = dAlias
		selectParts = append(selectParts,
			fmt.Sprintf("count(*) FILTER (WHERE %s IS NULL) AS %s", ident, nAlias),
			fmt.Sprintf("approx_count_distinct(%s) AS %s", ident, dAlias),
		)
	}

	query := fmt.Sprintf("SELECT %s FROM read_parquet(%s)", strings.Join(selectParts, ", "), sqlQuote(parquetPath))
	rows, err := h.Query(ctx, query)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("profiling: computing column statistics: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, nil, nil, fmt.Errorf("profiling: no result returned computing column statistics")
	}

	numCols := 1 + 2*len(schema)
	dest := make([]any, numCols)
	ptrs := make([]any, numCols)
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return 0, nil, nil, fmt.Errorf("profiling: scanning column statistics: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, fmt.Errorf("profiling: reading column statistics: %w", err)
	}

	totalRows := asInt64(dest[0])
	nullCounts := make(map[string]int64, len(schema))
	distinctCounts := make(map[string]int64, len(schema))
	idx := 1
	for _, col := range schema {
		nullCounts[col.name] = asInt64(dest[idx])
		distinctCounts[col.name] = asInt64(dest[idx+1])
		idx += 2
	}

	return totalRows, nullCounts, distinctCounts, nil
}

// mapDuckDBType maps a DuckDB column type name (as reported by DESCRIBE) to schema.md's
// InferredType enum. Falls back to "string" for any type this product does not otherwise
// distinguish (BLOB, structured/nested types, etc.) — a safe, inspectable default rather than an
// error, since ingest must not fail merely because a column's type is unusual.
func mapDuckDBType(duckType string) domain.InferredType {
	t := strings.ToUpper(strings.TrimSpace(duckType))
	switch {
	case t == "BOOLEAN":
		return domain.TypeBoolean
	case t == "DATE":
		return domain.TypeDate
	case strings.HasPrefix(t, "TIMESTAMP"):
		return domain.TypeTimestamp
	case isIntegerType(t):
		return domain.TypeInteger
	case isNumberType(t):
		return domain.TypeNumber
	default:
		return domain.TypeString
	}
}

func isIntegerType(t string) bool {
	switch t {
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT",
		"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT":
		return true
	}
	return false
}

func isNumberType(t string) bool {
	if t == "DOUBLE" || t == "FLOAT" || t == "REAL" {
		return true
	}
	return strings.HasPrefix(t, "DECIMAL") || strings.HasPrefix(t, "NUMERIC")
}

// asString converts a value scanned from an engine.Rows generic column into a string, handling the
// []byte-vs-string variance common across database/sql drivers.
func asString(v any) string {
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

// asInt64 converts a value scanned from an engine.Rows generic column into an int64, handling the
// range of integer/unsigned/float Go types database/sql drivers hand back for aggregate results.
func asInt64(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int32:
		return int64(val)
	case int:
		return int64(val)
	case uint64:
		return int64(val)
	case uint32:
		return int64(val)
	case float64:
		return int64(val)
	case []byte:
		var n int64
		_, _ = fmt.Sscanf(string(val), "%d", &n)
		return n
	default:
		return 0
	}
}

// quoteIdent double-quotes a SQL identifier, doubling any embedded quote. Column names here come
// from the file's own normalized schema, not end-user SQL input, but this still avoids building
// unescaped SQL out of external data (matching ingest/loaders.go's convention).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqlQuote single-quotes a SQL string literal, doubling any embedded quote. See
// ingest/loaders.go's normalizeToParquet doc comment for why paths are embedded as literals here
// rather than bind parameters: DuckDB's table functions need the path at plan time.
func sqlQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
