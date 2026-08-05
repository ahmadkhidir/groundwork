// Package pii detects likely-sensitive columns at ingest — emails, national IDs, card-like numbers,
// and personal names — per FEAT-007 (PII Detection & Flagging at Ingest).
//
// Detection combines two signals:
//  1. A cheap, always-run column-NAME heuristic, which works regardless of the column's inferred
//     type (e.g. a "national_id" column stored as an integer still gets flagged, which a value-shape
//     regex over strings alone would miss).
//  2. A value-SAMPLE regex check, run only for string-typed columns the name heuristic didn't already
//     flag, since a real email/card/SSN shape only ever shows up in string data once normalized.
//
// Detection is deliberately conservative toward false positives rather than false negatives: a
// column flagged in error costs the user one click to clear on screens/07-ingest-confirmation.md
// (FEAT-007's acceptance criterion "user can review... and override the flag"), while a real PII
// column silently left unflagged defeats the feature outright.
//
// A flagged column defaults to name-and-type-only exposure to the model — that egress behavior is
// enforced where model payloads are actually built (Phase 2, FEAT-013+), not here. This package's
// only job is deciding pii_flag/pii_pattern. It is a distinct control from
// domain.Column.ExcludedFromModel (FEAT-010, also Phase 2+): that flag hides a column's name AND
// values from every payload; a PII flag alone still allows the column's NAME to be sent, just not its
// values. Conflating the two here would silently widen FEAT-010's stronger guarantee onto every
// PII-flagged column, which is not what either feature specifies.
//
// Runs through the Engine Provider seam exclusively (an Execer, satisfied by engine.Handle), matching
// internal/profiling and internal/relationships.
package pii

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// Execer is the subset of engine.Handle this package needs.
type Execer interface {
	Query(ctx context.Context, sql string, args ...any) (engine.Rows, error)
}

// sampleSize bounds how many non-null values are read per string column when checking value shape —
// enough to detect a consistent format without scanning an entire large column. Mirrors
// relationships.overlapSampleSize's rationale, at a smaller size since this only needs to recognize a
// format, not compute an overlap ratio between two full columns.
const sampleSize = 200

// matchThreshold is the fraction of sampled non-empty values that must match a pattern's shape for
// the column to be flagged from its VALUES. Guards against a handful of coincidental matches in an
// otherwise unrelated column (e.g. one row that happens to look like an email inside a free-text
// notes column) triggering a flag for the whole column.
const matchThreshold = 0.6

// Pattern names, matching schema.md's Column.pii_pattern examples ("email", "national_id",
// "card_number", "name") plus "manual" for a flag the user sets that detection did not itself infer
// (see the confirmation API's PATCH handler).
const (
	PatternEmail      = "email"
	PatternNationalID = "national_id"
	PatternCardNumber = "card_number"
	PatternName       = "name"
	PatternManual     = "manual"
)

// nameHints maps a column-name substring (matched case-insensitively) to the pattern it implies.
// Ordered most-specific first. "name" alone is deliberately absent — too generic a substring (it
// would match "username", "filename", "company_name" indiscriminately) — in favor of the compound
// forms below it, which are what real-world exports actually use for a personal name column.
var nameHints = []struct {
	substr  string
	pattern string
}{
	{"email", PatternEmail},
	{"e_mail", PatternEmail},
	{"ssn", PatternNationalID},
	{"social_security", PatternNationalID},
	{"national_id", PatternNationalID},
	{"passport", PatternNationalID},
	{"tax_id", PatternNationalID},
	{"credit_card", PatternCardNumber},
	{"card_number", PatternCardNumber},
	{"cc_number", PatternCardNumber},
	{"first_name", PatternName},
	{"last_name", PatternName},
	{"full_name", PatternName},
	{"middle_name", PatternName},
	{"customer_name", PatternName},
	{"contact_name", PatternName},
	{"employee_name", PatternName},
}

var (
	emailValuePattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[a-zA-Z]{2,}$`)
	// A US-shaped SSN (###-##-####) is the one national-ID VALUE format common enough across
	// English-language sample data to detect reliably without country-specific configuration; other
	// national ID formats vary too widely to detect from values alone and rely on the name heuristic
	// above instead.
	ssnValuePattern = regexp.MustCompile(`^\d{3}-\d{2}-\d{4}$`)
	// Matches a run of 13-19 digits after stripping common card-number separators — the standard PAN
	// length range (Visa/Mastercard 16, Amex 15, some debit networks up to 19).
	cardValuePattern    = regexp.MustCompile(`^[0-9]{13,19}$`)
	cardValueSeparators = regexp.MustCompile(`[ -]`)
)

// ColumnInput is the minimal per-column information DetectAll needs.
type ColumnInput struct {
	Name         string
	InferredType domain.InferredType
}

// Result is one column's PII detection outcome, matching domain.Column's PIIFlag/PIIPattern fields.
type Result struct {
	Flagged bool
	Pattern string
}

// DetectAll runs PII detection for every column in cols, keyed by column name (unique within a
// dataset per the columns table's UNIQUE(dataset_id, name) constraint, so a name key is safe here).
func DetectAll(ctx context.Context, h Execer, parquetPath string, cols []ColumnInput) (map[string]Result, error) {
	out := make(map[string]Result, len(cols))
	for _, c := range cols {
		if flagged, pattern := detectFromName(c.Name); flagged {
			out[c.Name] = Result{Flagged: true, Pattern: pattern}
			continue
		}
		if c.InferredType != domain.TypeString {
			out[c.Name] = Result{}
			continue
		}

		samples, err := sampleValues(ctx, h, parquetPath, c.Name)
		if err != nil {
			return nil, fmt.Errorf("pii: sampling %q: %w", c.Name, err)
		}
		flagged, pattern := detectFromSamples(samples)
		out[c.Name] = Result{Flagged: flagged, Pattern: pattern}
	}
	return out, nil
}

// detectFromName checks a column's name alone against nameHints.
func detectFromName(name string) (bool, string) {
	n := strings.ToLower(name)
	for _, hint := range nameHints {
		if strings.Contains(n, hint.substr) {
			return true, hint.pattern
		}
	}
	return false, ""
}

// detectFromSamples checks a sample of a string column's values against known PII value shapes.
// Each value counts toward at most one pattern (via the switch below), so a column can never be
// simultaneously "matched" into two different patterns' thresholds from the same sample.
func detectFromSamples(samples []string) (bool, string) {
	var nonEmpty, emailMatches, ssnMatches, cardMatches int

	for _, raw := range samples {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		nonEmpty++

		switch {
		case emailValuePattern.MatchString(v):
			emailMatches++
		case ssnValuePattern.MatchString(v):
			ssnMatches++
		case cardValuePattern.MatchString(cardValueSeparators.ReplaceAllString(v, "")):
			cardMatches++
		}
	}

	if nonEmpty == 0 {
		return false, ""
	}

	switch {
	case float64(emailMatches)/float64(nonEmpty) >= matchThreshold:
		return true, PatternEmail
	case float64(ssnMatches)/float64(nonEmpty) >= matchThreshold:
		return true, PatternNationalID
	case float64(cardMatches)/float64(nonEmpty) >= matchThreshold:
		return true, PatternCardNumber
	default:
		return false, ""
	}
}

// sampleValues reads up to sampleSize non-null values of columnName from parquetPath, cast to
// VARCHAR so numeric-looking string columns (zero-padded ids, etc.) compare consistently.
func sampleValues(ctx context.Context, h Execer, parquetPath, columnName string) ([]string, error) {
	query := fmt.Sprintf(
		"SELECT CAST(%s AS VARCHAR) FROM read_parquet(%s) WHERE %s IS NOT NULL LIMIT %d",
		quoteIdent(columnName), sqlQuote(parquetPath), quoteIdent(columnName), sampleSize,
	)
	rows, err := h.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying sample values: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scanning sample value: %w", err)
		}
		out = append(out, asString(v))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading sample values: %w", err)
	}
	return out, nil
}

// asString converts a value scanned from an engine.Rows generic column into a string, mirroring
// profiling.asString / relationships' equivalent handling of the []byte-vs-string driver variance.
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

// quoteIdent double-quotes a SQL identifier, doubling any embedded quote.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqlQuote single-quotes a SQL string literal, doubling any embedded quote.
func sqlQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
