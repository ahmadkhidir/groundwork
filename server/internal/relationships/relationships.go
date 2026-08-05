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
			if confidence < MinConfidenceToPersist {
				continue
			}

			out = append(out, domain.CandidateJoin{
				SourceColumnID:  colA.ID,
				TargetColumnID:  colB.ID,
				ConfidenceScore: confidence,
				Basis:           basis(colA.Name, colB.Name, nameScore, typeScore, overlapPct, cardinalityScore, colA.InferredType, colB.InferredType),
				Status:          domain.JoinPending,
			})
		}
	}

	return out, nil
}

// basis renders the plain-language rationale schema.md's CandidateJoin.Basis requires — "a confidence
// number alone is not an explanation." Only signals that meaningfully contributed are mentioned.
func basis(nameA, nameB string, nameScore, typeScore, overlapPct, cardinalityScore float64, typeA, typeB domain.InferredType) string {
	var parts []string

	switch {
	case nameA == nameB:
		parts = append(parts, fmt.Sprintf("identical column name (%q)", nameA))
	case nameScore >= 0.7:
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

	if cardinalityScore >= 0.9 {
		parts = append(parts, "one side is nearly unique, consistent with a key")
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
