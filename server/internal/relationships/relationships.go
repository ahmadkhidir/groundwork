// Package relationships infers candidate joins between two already-profiled datasets, per FEAT-008
// (Multi-File Relationship Inference): name similarity, type compatibility, value overlap sampling,
// and cardinality checks combine into a confidence score plus a plain-language basis. Every result is
// a domain.CandidateJoin with Status "pending" — this package never decides a join is real, per
// schema.md's CandidateJoin doc comment ("Never auto-applied").
//
// Runs through the Engine Provider seam exclusively (an Execer, satisfied by engine.Handle) — see
// profiling's package doc comment for why.
package relationships

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// Execer is the subset of engine.Handle relationship inference needs.
type Execer interface {
	Query(ctx context.Context, sql string, args ...any) (engine.Rows, error)
}

// DatasetInput is one already-profiled dataset's inputs to relationship inference: its normalized
// Parquet (for value-overlap sampling) and its already-computed Column rows (for name/type/cardinality
// signals, so this package never recomputes what profiling already stored).
type DatasetInput struct {
	DatasetID   string
	ParquetPath string
	RowCount    int64
	Columns     []domain.Column
}

// MinConfidenceToPersist is the floor below which a candidate join is considered noise rather than a
// genuine candidate worth showing the user — features.md doesn't specify a cutoff, so this is
// documented here as the single, inspectable place that decision lives (never inlined per-call-site).
const MinConfidenceToPersist = 0.35

// UniqueCardinalityThreshold is the uniqueness ratio (see Uniqueness) above which a column is
// treated as key-like when the confirmation API (FEAT-009's accept endpoint) defaults a confirmed
// Relationship's cardinality. Documented here, alongside MinConfidenceToPersist, as the single place
// this kind of inference tunable lives — never inlined at the call site.
const UniqueCardinalityThreshold = 0.95

// ValueOverlapOnlyCeiling is the highest confidence a candidate join may report when its only
// meaningful evidence is value overlap on a non-identifier-shaped numeric column.
//
// features.md FEAT-008 makes this a hard, presentation-facing requirement rather than a tunable:
// such a join "must be capped at MEDIUM, never reported as HIGH", because two unrelated numeric
// measure columns overlapping in value by chance is common and is not, on its own, evidence of a
// relationship. schema.md's bands are HIGH >= 0.85 / MEDIUM 0.6-0.84 / LOW < 0.6, so this sits at the
// top of MEDIUM. Raising it to 0.85 or above would silently void the rule.
const ValueOverlapOnlyCeiling = 0.84

// meaningfulNameSimilarity is the name-similarity score at or above which two column names count as
// real evidence in their own right, rather than coincidence. Shared with basis(), which uses the same
// threshold to decide whether to mention names in its explanation at all — the cap and the
// user-facing rationale therefore always agree about what the evidence was.
const meaningfulNameSimilarity = 0.7

// pruneFloor is the combined name+type signal below which a column pair is skipped entirely, before
// ever running a value-overlap query. Bounds cost to roughly (columns in A) x (columns in B)
// candidate pairs actually worth sampling, rather than every pair unconditionally — a wide pair of
// datasets would otherwise pay one value-overlap query per (colA, colB) combination regardless of how
// implausible the pair is.
const pruneFloor = 0.25

// Infer proposes candidate joins between every plausible column pair across a and b. Returned
// CandidateJoins have SourceColumnID/TargetColumnID and ConfidenceScore/Basis populated; ID,
// WorkspaceID, Status, and CreatedAt are the caller's responsibility to fill in at persistence time
// (this package has no store dependency of its own).
func Infer(ctx context.Context, h Execer, a, b DatasetInput) ([]domain.CandidateJoin, error) {
	var out []domain.CandidateJoin

	for _, colA := range a.Columns {
		for _, colB := range b.Columns {
			nameScore := nameSimilarity(colA.Name, colB.Name)
			typeScore := typeCompatibility(colA.InferredType, colB.InferredType)

			if 0.6*nameScore+0.4*typeScore < pruneFloor {
				continue
			}

			overlapScore, overlapPct, err := sampleValueOverlap(ctx, h, a.ParquetPath, colA.Name, b.ParquetPath, colB.Name)
			if err != nil {
				return nil, fmt.Errorf("relationships: sampling overlap for %s.%s / %s.%s: %w",
					a.DatasetID, colA.Name, b.DatasetID, colB.Name, err)
			}

			cardinalityScore := math.Max(
				uniqueness(colA.DistinctCount, a.RowCount),
				uniqueness(colB.DistinctCount, b.RowCount),
			)

			confidence := 0.30*nameScore + 0.20*typeScore + 0.35*overlapScore + 0.15*cardinalityScore
			if confidence > 1 {
				confidence = 1
			}

			capped := valueOverlapOnly(colA, colB, a.RowCount, b.RowCount, nameScore)
			if capped && confidence > ValueOverlapOnlyCeiling {
				confidence = ValueOverlapOnlyCeiling
			}

			// When the cap applies, record whether the measure-name rule specifically is why a
			// near-unique column failed to earn the exemption — FEAT-008 requires basis to say so in
			// those words rather than only reporting the generic cap.
			var measureName string
			if capped {
				measureName = measureDefeatedExemption(colA, colB, a.RowCount, b.RowCount)
			}

			if confidence < MinConfidenceToPersist {
				continue
			}

			out = append(out, domain.CandidateJoin{
				SourceColumnID:  colA.ID,
				TargetColumnID:  colB.ID,
				ConfidenceScore: confidence,
				Basis:           basis(colA.Name, colB.Name, nameScore, typeScore, overlapPct, cardinalityScore, colA.InferredType, colB.InferredType, capped, measureName),
				Status:          domain.JoinPending,
			})
		}
	}

	return out, nil
}

// valueOverlapOnly reports whether FEAT-008's MEDIUM cap applies to this pair: the only meaningful
// evidence is value overlap on a column that is not identifier-shaped.
//
// Three conditions, all taken directly from FEAT-008's wording, all required:
//
//  1. Both sides are numeric. The rule is scoped to "a non-identifier-shaped NUMERIC column" —
//     overlapping text values (a name, a country code, an account reference) are not the coincidence
//     this cap exists to suppress, and widening it to them would invent a rule the doc doesn't state.
//  2. Neither side is identifier-shaped, where FEAT-008 defines identifier-shaped as
//     "`id`/`key`/`code`-named or near-unique" — see identifierShaped and Uniqueness.
//  3. The names are not meaningfully similar. When two columns share a name (or near enough), name
//     similarity is independent evidence, so value overlap is no longer the ONLY evidence and the
//     rule's precondition fails.
//
// Type compatibility deliberately does not count as evidence here: every numeric column is
// type-compatible with every other numeric column, so it distinguishes nothing among exactly the
// pairs this cap is about.
func valueOverlapOnly(colA, colB domain.Column, rowCountA, rowCountB int64, nameScore float64) bool {
	if !isNumeric(colA.InferredType) || !isNumeric(colB.InferredType) {
		return false
	}
	if identifierShaped(colA, rowCountA) || identifierShaped(colB, rowCountB) {
		return false
	}
	return nameScore < meaningfulNameSimilarity
}

// isNumeric reports whether t is one of schema.md's numeric inferred types.
func isNumeric(t domain.InferredType) bool {
	return t == domain.TypeInteger || t == domain.TypeNumber
}

// identifierTokens are FEAT-008's "identifier-named" tokens: `id`/`key`/`code`.
var identifierTokens = []string{"id", "key", "code"}

// measureTokens are FEAT-008's measure-shaped names — the words real exports use for money and
// quantity columns. A column whose name matches one of these cannot claim the near-unique exemption
// "regardless of its actual distinctness": every invoice happening to have a distinct total is a
// property of the data, not evidence that `total` is a key.
var measureTokens = []string{
	"total", "amount", "price", "qty", "quantity", "balance",
	"sum", "cost", "revenue", "fee", "tax", "discount",
}

// matchesNameToken reports whether col's normalized name matches any of tokens as a WHOLE NAME or a
// `_`-delimited affix — the shapes real exports use (`id`, `customer_id`, `id_customer`,
// `order_key`, `branch_code`, `line_total`, `tax_rate`).
//
// Deliberately not a substring match anywhere in the name. Substring matching would read `code`
// inside `discount_code_value` fine but also read `id` inside `paid`, `void`, or `residual` — and,
// on the measure list, would read `tax` inside `taxonomy`. FEAT-008 calls out `paid`/`void`/
// `residual`/`taxonomy` by name as columns that must NOT match, so both lists share this one
// matching discipline rather than each inventing its own.
func matchesNameToken(name string, tokens []string) bool {
	normalized := normalizeName(name)
	for _, token := range tokens {
		if normalized == token ||
			strings.HasPrefix(normalized, token+"_") ||
			strings.HasSuffix(normalized, "_"+token) {
			return true
		}
	}
	return false
}

// identifierNamed reports FEAT-008's identifier-named branch: an `id`/`key`/`code` name.
func identifierNamed(col domain.Column) bool {
	return matchesNameToken(col.Name, identifierTokens)
}

// measureShaped reports whether col's name is measure-shaped per FEAT-008's list.
func measureShaped(col domain.Column) bool {
	return matchesNameToken(col.Name, measureTokens)
}

// nearUnique reports whether col's distinct/row ratio reaches UniqueCardinalityThreshold — the same
// key-like signal the confirmation API already uses to default a confirmed relationship's
// cardinality, rather than a second definition of "looks like a key".
func nearUnique(col domain.Column, rowCount int64) bool {
	return uniqueness(col.DistinctCount, rowCount) >= UniqueCardinalityThreshold
}

// identifierShaped reports whether col looks like an identifier rather than a measure, per FEAT-008.
// It is a NAME-based test first, checked before uniqueness is ever consulted, with two independent
// exempting branches:
//
//   - Identifier-named: `id`/`key`/`code` as a whole name or `_`-delimited affix.
//   - Near-unique: distinct/row ratio >= UniqueCardinalityThreshold AND the name is not
//     measure-shaped. FEAT-008: "Uniqueness alone is no longer sufficient" — a measure-shaped name
//     defeats this branch outright, "regardless of its actual distinctness", closing the observed
//     case where a `total` column with 40 distinct values over 40 rows escaped the cap and reported
//     as key-like on uniqueness alone.
//
// Note the two branches are independent: an identifier-NAMED column is exempt even if its values
// repeat heavily (`branch_code`), and a measure-shaped name only defeats the near-unique branch — it
// never overrides an explicit identifier name, so a hypothetical `total_id` stays exempt.
func identifierShaped(col domain.Column, rowCount int64) bool {
	if identifierNamed(col) {
		return true
	}
	return nearUnique(col, rowCount) && !measureShaped(col)
}

// measureDefeatedExemption returns the name of the column, if any, whose near-unique exemption was
// defeated specifically by the measure-name rule — i.e. it is near-unique, is not identifier-named,
// and carries a measure-shaped name. Returns "" when no side is in that position.
//
// This exists purely so basis can use FEAT-008's required wording for that case ("capped:
// measure-shaped name (`total`), near-unique exemption does not apply") rather than the generic cap
// sentence, which would leave a user staring at a near-unique money column with no explanation of
// why its uniqueness bought it nothing.
func measureDefeatedExemption(colA, colB domain.Column, rowCountA, rowCountB int64) string {
	for _, side := range []struct {
		col      domain.Column
		rowCount int64
	}{{colA, rowCountA}, {colB, rowCountB}} {
		if !identifierNamed(side.col) && nearUnique(side.col, side.rowCount) && measureShaped(side.col) {
			return side.col.Name
		}
	}
	return ""
}

// basis renders the plain-language rationale schema.md's CandidateJoin.Basis requires — "a confidence
// number alone is not an explanation." Only signals that meaningfully contributed are mentioned.
//
// capped adds FEAT-008's required disclosure: when the MEDIUM cap applies, the basis must say so, so
// a user reading "78% sampled value overlap" is also told why that overlap is not being treated as a
// strong signal. measureName, when non-empty, names the column whose near-unique exemption the
// measure-name rule specifically defeated, which FEAT-008 requires be stated in those terms.
func basis(nameA, nameB string, nameScore, typeScore, overlapPct, cardinalityScore float64, typeA, typeB domain.InferredType, capped bool, measureName string) string {
	var parts []string

	switch {
	case nameA == nameB:
		parts = append(parts, fmt.Sprintf("identical column name (%q)", nameA))
	case nameScore >= meaningfulNameSimilarity:
		parts = append(parts, fmt.Sprintf("similar column names (%q / %q)", nameA, nameB))
	}

	if typeScore >= 0.7 {
		if typeA == typeB {
			parts = append(parts, fmt.Sprintf("matching type (%s)", typeA))
		} else {
			parts = append(parts, fmt.Sprintf("compatible types (%s / %s)", typeA, typeB))
		}
	}

	if overlapPct > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% sampled value overlap", overlapPct))
	}

	// Suppressed when the measure-name rule fired: claiming "consistent with a key" in the same
	// breath as "the near-unique exemption does not apply" is the exact contradiction the live bug
	// showed a user — a `total` column scored on uniqueness with that sentence as its justification.
	if cardinalityScore >= 0.9 && measureName == "" {
		parts = append(parts, "one side is nearly unique, consistent with a key")
	}

	switch {
	case measureName != "":
		parts = append(parts, fmt.Sprintf(
			"capped at medium: measure-shaped name (%q), near-unique exemption does not apply — a "+
				"money or quantity column having distinct values is not evidence it is a key",
			measureName))
	case capped:
		parts = append(parts,
			"capped at medium: value overlap only, not an identifier column — two numeric measure "+
				"columns can overlap by chance, so this is never reported as high confidence")
	}

	if len(parts) == 0 {
		return "weak combined signal across name, type, and sampled values"
	}
	return strings.Join(parts, "; ")
}

// uniqueness scores how "key-like" a column looks: distinct values close to the row count reads as a
// primary/foreign key shape, which is one of the cardinality signals features.md asks for.
func uniqueness(distinctCount, rowCount int64) float64 {
	if rowCount <= 0 {
		return 0
	}
	ratio := float64(distinctCount) / float64(rowCount)
	if ratio > 1 {
		ratio = 1
	}
	return ratio
}

// Uniqueness is uniqueness's exported form, reused by the confirmation API (FEAT-009's accept
// endpoint) to default a confirmed Relationship's cardinality from the same key-like signal
// relationship inference already computes — rather than reimplementing it.
func Uniqueness(distinctCount, rowCount int64) float64 {
	return uniqueness(distinctCount, rowCount)
}

// normalizeName lowercases and collapses separators so "Customer ID", "customer-id", and
// "customer_id" all compare equal.
func normalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// nameSimilarity scores 0-1 how similar two column names are, after normalization. Exact match
// (after normalization) scores 1; a foreign-key shape like "customer_id" vs. "id" scores highly via
// the suffix check; otherwise falls back to a normalized Levenshtein ratio.
func nameSimilarity(a, b string) float64 {
	na, nb := normalizeName(a), normalizeName(b)
	if na == nb {
		return 1
	}
	if na == "" || nb == "" {
		return 0
	}

	// Classic foreign-key shape: "customer_id" referencing a column literally named "id", or vice
	// versa.
	if (strings.HasSuffix(na, "_id") && strings.TrimSuffix(na, "_id") != "" && nb == "id") ||
		(strings.HasSuffix(nb, "_id") && strings.TrimSuffix(nb, "_id") != "" && na == "id") {
		return 0.85
	}

	dist := levenshtein(na, nb)
	maxLen := len(na)
	if len(nb) > maxLen {
		maxLen = len(nb)
	}
	if maxLen == 0 {
		return 0
	}
	sim := 1 - float64(dist)/float64(maxLen)
	if sim < 0 {
		sim = 0
	}
	return sim
}

// levenshtein computes the edit distance between a and b.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// typeCompatibility scores 0-1 how compatible two inferred types are for joining.
func typeCompatibility(a, b domain.InferredType) float64 {
	if a == b {
		return 1
	}
	numeric := map[domain.InferredType]bool{domain.TypeInteger: true, domain.TypeNumber: true}
	if numeric[a] && numeric[b] {
		return 0.8
	}
	temporal := map[domain.InferredType]bool{domain.TypeDate: true, domain.TypeTimestamp: true}
	if temporal[a] && temporal[b] {
		return 0.7
	}
	return 0
}

// sampleValueOverlap estimates the fraction of distinct sampled values in the smaller column that
// also appear in the other column, by casting both sides to VARCHAR for comparison — this lets, say,
// an integer "1002" and a string "1002" from a differently-typed export still be recognized as the
// same key, which is common across real-world file exports of the same underlying id. Bounded to a
// fixed sample size so this never scans an entire large column.
const overlapSampleSize = 5000

func sampleValueOverlap(ctx context.Context, h Execer, pathA, colA, pathB, colB string) (score, percent float64, err error) {
	query := fmt.Sprintf(`
		WITH a AS (
			SELECT DISTINCT CAST(%s AS VARCHAR) AS v FROM read_parquet(%s) WHERE %s IS NOT NULL LIMIT %d
		), b AS (
			SELECT DISTINCT CAST(%s AS VARCHAR) AS v FROM read_parquet(%s) WHERE %s IS NOT NULL LIMIT %d
		)
		SELECT
			(SELECT count(*) FROM a) AS a_count,
			(SELECT count(*) FROM b) AS b_count,
			(SELECT count(*) FROM a JOIN b USING (v)) AS overlap
	`,
		quoteIdent(colA), sqlQuote(pathA), quoteIdent(colA), overlapSampleSize,
		quoteIdent(colB), sqlQuote(pathB), quoteIdent(colB), overlapSampleSize,
	)

	rows, err := h.Query(ctx, query)
	if err != nil {
		return 0, 0, fmt.Errorf("querying value overlap: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, 0, fmt.Errorf("no result returned computing value overlap")
	}

	var aCount, bCount, overlap any
	if err := rows.Scan(&aCount, &bCount, &overlap); err != nil {
		return 0, 0, fmt.Errorf("scanning value overlap: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("reading value overlap: %w", err)
	}

	aN, bN, oN := asInt64(aCount), asInt64(bCount), asInt64(overlap)
	smaller := aN
	if bN < smaller {
		smaller = bN
	}
	if smaller <= 0 {
		return 0, 0, nil
	}

	ratio := float64(oN) / float64(smaller)
	if ratio > 1 {
		ratio = 1
	}
	return ratio, ratio * 100, nil
}

// asInt64 converts a value scanned from an engine.Rows generic column into an int64, mirroring
// profiling.asInt64 — duplicated locally rather than shared across these two small, independent
// packages to avoid a needless cross-package dependency for one helper.
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

// quoteIdent double-quotes a SQL identifier, doubling any embedded quote.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqlQuote single-quotes a SQL string literal, doubling any embedded quote.
func sqlQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
