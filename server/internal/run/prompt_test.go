package run

import (
	"strings"
	"testing"
)

// TestGenerateSQLPromptRequiresQuotedIdentifiers pins the quoting rule that makes generated SQL
// survive real-world column names. A MetaTrader-style export headers its columns `<DATE>`,
// `<HIGH>`, etc.; those are LITERAL column names (not placeholders), and DuckDB only parses them
// when the identifier is double-quoted — `"<HIGH>"`. Without the rule, a model referencing `<HIGH>`
// unquoted produces a parse error at execute ("syntax error at or near '<'"), the exact failure
// observed with this data. The prompt is the primary mitigation — there is no substitution or
// re-quoting step after generation — so this guard keeps the instruction from being trimmed as
// noise in a future edit.
func TestGenerateSQLPromptRequiresQuotedIdentifiers(t *testing.T) {
	lower := strings.ToLower(generateSQLSystemPrompt)
	for _, phrase := range []string{"double-quoted", "literal", "parse error"} {
		if !strings.Contains(lower, phrase) {
			t.Errorf("generateSQLSystemPrompt must require double-quoted identifiers: missing %q", phrase)
		}
	}
	// The exact-names rule (the positive instruction) must survive.
	if !strings.Contains(lower, "never invent table or column names") {
		t.Errorf("generateSQLSystemPrompt must keep the exact-names requirement")
	}
}
