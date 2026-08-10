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
	Name         string
	InferredType domain.InferredType
	// TypeFitRates is the 0-1 fit rate for EVERY candidate type, not just the inferred one, so the
	// confirmation screen can show a rate for whichever type the user selects without going back to
	// the engine. Computed here because the scan is already happening: one more FILTER clause per
	// candidate type costs no extra round trip, whereas recomputing on override would mean waking a
	// possibly-sleeping engine to redraw a badge (see FEAT-005's "stored, not recomputed on every
	// use" criterion, and FEAT-011 on not billing engine-seconds for a glance).
	TypeFitRates map[domain.InferredType]float64
	// TypeConfidence is TypeFitRates[InferredType] — the rate for the type actually inferred. Kept as
	// its own field because that is the value persisted on the column and shown until a user
	// overrides the type.
	TypeConfidence float64
	NullRate       float64
	DistinctCount  int64
}

// candidateTypes is every type a user may select on screens/07's Types step, and therefore every
// type a fit rate is computed for. Ordered for stable SQL generation.
var candidateTypes = []domain.InferredType{
	domain.TypeString,
	domain.TypeInteger,
	domain.TypeNumber,
	domain.TypeBoolean,
	domain.TypeDate,
	domain.TypeTime,
	domain.TypeTimestamp,
}

// nullRatePenalty is how much a fully-null column's confidence is docked. FEAT-005: the agreement
// rate is "weakened by the column's null rate as before (a mostly-empty column gave the sniffer
// little to work with)". Tunable without a docs change; named here rather than inlined.
const nullRatePenalty = 0.2

// confidenceFloor keeps a confidence from being reported as ~0, which reads as "no opinion" rather
// than the low-but-real opinion it is. FEAT-005 fixes that a floor exists; its value is tunable. It
// sits inside the LOW band (<0.6) so a floored column still reads LOW to the user.
const confidenceFloor = 0.4

// stricterThanString are the types DuckDB's sniffer tries and rejects before falling back to VARCHAR,
// expressed as the SQL types a value would have to coerce to for that rejection to look doubtful.
// Used only for the string branch of the agreement measure (see agreementExpr).
//
// DOUBLE subsumes the integer check: any value that coerces to an integer also coerces to a double,
// and the string branch only asks "could this have been something stricter?", not which stricter
// type it would have been.
//
// TIME is included because a bare time-of-day value (`09:30:00`) is exactly the column that
// otherwise looks like free text here: it coerces to neither DOUBLE, BOOLEAN, TIMESTAMP, nor DATE,
// so without TIME its string fit rate would read HIGH — the silent-wrongness the time type exists
// to prevent (trading data splits date and time into separate columns).
var stricterThanString = []string{"DOUBLE", "BOOLEAN", "TIMESTAMP", "TIME", "DATE"}

// coercionTargetSQL maps schema.md's InferredType enum to the SQL type a value must coerce to for it
// to count as agreeing with that inference.
//
// Integer maps to HUGEINT rather than BIGINT deliberately: DuckDB's integer family runs up to
// HUGEINT/UHUGEINT, and casting a wide column down to BIGINT would report overflow as type
// disagreement — a false LOW on a column nothing is actually ambiguous about.
var coercionTargetSQL = map[domain.InferredType]string{
	domain.TypeBoolean:   "BOOLEAN",
	domain.TypeDate:      "DATE",
	domain.TypeTime:      "TIME",
	domain.TypeTimestamp: "TIMESTAMP",
	domain.TypeInteger:   "HUGEINT",
	domain.TypeNumber:    "DOUBLE",
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

	agg, err := aggregateStats(ctx, h, parquetPath, schema)
	if err != nil {
		return nil, err
	}

	stats := make([]ColumnStat, 0, len(schema))
	for _, col := range schema {
		var nullRate float64
		if agg.totalRows > 0 {
			nullRate = float64(agg.nullCounts[col.name]) / float64(agg.totalRows)
		}

		nonNull := agg.totalRows - agg.nullCounts[col.name]

		fitRates := make(map[domain.InferredType]float64, len(candidateTypes))
		for _, candidate := range candidateTypes {
			fitRates[candidate] = typeConfidence(agg.agreeCounts[col.name][candidate], nonNull, nullRate)
		}

		stats = append(stats, ColumnStat{
			Name:         col.name,
			InferredType: col.inferredType,
			TypeFitRates: fitRates,
			// Identical by construction to fitRates[inferredType] — the inferred type is not a special
			// case of the measurement, only the one selected by default.
			TypeConfidence: fitRates[col.inferredType],
			NullRate:       nullRate,
			DistinctCount:  agg.distinctCounts[col.name],
		})
	}
	return stats, nil
}

// typeConfidence turns one candidate type's measured agreement count into FEAT-005's 0-1 fit rate.
//
// FEAT-005 specifies WHAT this number measures — "type-inference agreement across the sampled
// non-null values, not simply which type the sniffer picked" — because a fixed prior per inferred
// type (the previous implementation) lands every column in the HIGH band regardless of evidence,
// which makes the badge on screens/07 report which type was chosen rather than how much evidence
// there was, and invites skimming the review step that doc calls the highest-risk UX in the product.
//
// agreeCount is the number of non-null values that agree with the candidate type being scored; see
// agreementExpr for how agreement is defined per type. That rate is then weakened by null rate and floored, both
// of which FEAT-005 keeps.
//
// A column with no non-null values at all scores the floor: the sniffer had nothing to work with, so
// there is no evidence to be confident about.
func typeConfidence(agreeCount, nonNullCount int64, nullRate float64) float64 {
	agreement := 0.0
	if nonNullCount > 0 {
		agreement = float64(agreeCount) / float64(nonNullCount)
		if agreement > 1 {
			agreement = 1
		}
	}

	confidence := agreement - nullRate*nullRatePenalty
	if confidence < confidenceFloor {
		confidence = confidenceFloor
	}
	return confidence
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

// columnAggregates is everything the single combined aggregate query yields, keyed by column name.
type columnAggregates struct {
	totalRows      int64
	nullCounts     map[string]int64
	distinctCounts map[string]int64
	// agreeCounts is, per column, the number of non-null values agreeing with EACH candidate type —
	// the numerators of FEAT-005's fit rates. Keyed by column name, then by candidate type.
	agreeCounts map[string]map[domain.InferredType]int64
}

// agreementExpr builds the SQL aggregate that counts a column's non-null values agreeing with its
// inferred type — FEAT-005's measure, computed in the engine rather than by pulling values into Go.
//
// Two branches, exactly as FEAT-005 specifies:
//
//   - boolean/date/time/timestamp/integer/number: values that coerce to the inferred type without
//     loss. TRY_CAST yields NULL rather than erroring on failure, which is what makes this
//     expressible as a FILTER clause inside the existing combined query.
//   - string: DuckDB's fallback, chosen once every stricter type was tried and rejected. Agreement
//     is the fraction of values that do NOT also coerce to one of those stricter types. Free text
//     coerces to none of them and scores 1.0; a mostly-numeric column scores near 0, surfacing the
//     unresolved ambiguity the sniffer actually had instead of hiding it behind an always-HIGH badge.
//
// The inner TRY_CAST to VARCHAR is not redundant. mapDuckDBType routes anything unrecognized — BLOB,
// STRUCT, LIST — to string, and casting those directly to DOUBLE is a binder error that would fail
// the whole profiling query rather than returning NULL for that column.
// It takes a candidate type rather than reading col.inferredType, because FEAT-005 now measures a
// fit rate for every selectable type, not only the inferred one — the badge on screens/07 must be
// able to describe whichever type the user picks.
func agreementExpr(col columnSchema, candidate domain.InferredType) string {
	ident := quoteIdent(col.name)

	if target, ok := coercionTargetSQL[candidate]; ok {
		return fmt.Sprintf("count(*) FILTER (WHERE %s IS NOT NULL AND TRY_CAST(%s AS %s) IS NOT NULL)",
			ident, ident, target)
	}

	tests := make([]string, 0, len(stricterThanString))
	for _, target := range stricterThanString {
		tests = append(tests, fmt.Sprintf("TRY_CAST(TRY_CAST(%s AS VARCHAR) AS %s) IS NOT NULL", ident, target))
	}
	return fmt.Sprintf("count(*) FILTER (WHERE %s IS NOT NULL AND NOT (%s))",
		ident, strings.Join(tests, " OR "))
}

// aggregateStats runs one combined query computing the total row count plus a null count, an
// approximate distinct count, and a type-agreement count per column.
//
// Still ONE query, not one per column. FEAT-005 requires profiling to complete before the user's
// first question, and a per-column coercion query would turn a wide dataset into N sequential round
// trips through the engine. Because that single scan is already required, the "sample" of non-null
// values FEAT-005 measures agreement over is the whole column — the largest sample available at no
// extra round trip. Sample size is explicitly implementation-defined, so narrowing this to a LIMITed
// subsample later is a local change here.
func aggregateStats(ctx context.Context, h Execer, parquetPath string, schema []columnSchema) (columnAggregates, error) {
	var zero columnAggregates

	selectParts := []string{"count(*) AS __total_rows__"}
	for i, col := range schema {
		ident := quoteIdent(col.name)
		selectParts = append(selectParts,
			fmt.Sprintf("count(*) FILTER (WHERE %s IS NULL) AS __null_%d__", ident, i),
			// Exact, NOT approx_count_distinct. HyperLogLog is approximate by construction and was
			// observed returning 4 for a 5-value DOUBLE column — a 20% error on a trivial input.
			// That is wrong twice over: schema.md defines distinct_count as "Number of distinct
			// values in this column" with no notion of approximation and shows it to the user, and
			// the value feeds FEAT-008's near-unique (>=0.95) identifier test, where a small error
			// can flip a column between "identifier" and "measure" and change a join's confidence
			// band. A trust-facing number must not be an estimate. DuckDB computes exact distinct
			// counts columnar-fast; revisit only with a measured problem on real dataset sizes.
			fmt.Sprintf("count(DISTINCT %s) AS __distinct_%d__", ident, i),
		)
		// One agreement count per candidate type. This widens the projection from 3 aggregates per
		// column to 2+len(candidateTypes), but it is still exactly ONE query and ONE scan — the thing
		// that actually costs, and the reason this is measured here rather than on demand.
		for j, candidate := range candidateTypes {
			selectParts = append(selectParts,
				fmt.Sprintf("%s AS __agree_%d_%d__", agreementExpr(col, candidate), i, j))
		}
	}

	query := fmt.Sprintf("SELECT %s FROM read_parquet(%s)", strings.Join(selectParts, ", "), sqlQuote(parquetPath))
	rows, err := h.Query(ctx, query)
	if err != nil {
		return zero, fmt.Errorf("profiling: computing column statistics: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return zero, fmt.Errorf("profiling: no result returned computing column statistics")
	}

	// One total-rows column, then per column: null count, distinct count, and one agreement count per
	// candidate type. Must stay in step with aggregateStats' projection.
	numCols := 1 + (2+len(candidateTypes))*len(schema)
	dest := make([]any, numCols)
	ptrs := make([]any, numCols)
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return zero, fmt.Errorf("profiling: scanning column statistics: %w", err)
	}
	if err := rows.Err(); err != nil {
		return zero, fmt.Errorf("profiling: reading column statistics: %w", err)
	}

	agg := columnAggregates{
		totalRows:      asInt64(dest[0]),
		nullCounts:     make(map[string]int64, len(schema)),
		distinctCounts: make(map[string]int64, len(schema)),
		agreeCounts:    make(map[string]map[domain.InferredType]int64, len(schema)),
	}
	idx := 1
	for _, col := range schema {
		agg.nullCounts[col.name] = asInt64(dest[idx])
		agg.distinctCounts[col.name] = asInt64(dest[idx+1])
		idx += 2

		perType := make(map[domain.InferredType]int64, len(candidateTypes))
		for _, candidate := range candidateTypes {
			perType[candidate] = asInt64(dest[idx])
			idx++
		}
		agg.agreeCounts[col.name] = perType
	}

	return agg, nil
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
	// TIMESTAMP must be matched before the TIME cases: "TIMESTAMP..." starts with the substring
	// "TIME", and DuckDB's DESCRIBE reports the full type name.
	case strings.HasPrefix(t, "TIMESTAMP"):
		return domain.TypeTimestamp
	case t == "TIME" || strings.HasPrefix(t, "TIME "):
		// Bare time-of-day (TIME) and TIME WITH TIME ZONE (TIMETZ) — a split date/time pair in
		// trading-style data. Coerced to the product's `time` type, reviewable on screens/07.
		return domain.TypeTime
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
