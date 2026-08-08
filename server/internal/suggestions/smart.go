package suggestions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	"github.com/ahmadkhidir/groundwork/server/internal/run"
)

// SuggestionsSystemPrompt is the framing for FEAT-051's model call: rephrase and enrich the
// deterministic candidate questions into 5-10 data-informed questions, never inventing entities,
// and always returning a JSON array of strings. A suggestion is a question, never a claim — it may
// reference observed patterns (a peak month, a most-frequent category) only as the subject of an
// interrogative.
const SuggestionsSystemPrompt = `You are helping a user discover what they can ask about their uploaded data.
You are given a set of candidate questions generated from the data's schema, plus profiling output
for the safe columns (top values with counts, numeric ranges, top dates).

Return a JSON array of 5 to 10 strings — plain, natural-language questions the user could ask about
this data. Rephrase and enrich the candidates using the profiling output (e.g. mention a most
frequent category or a wide range as the SUBJECT of a question), but:

- Never invent a column, dataset, metric, or relationship that is not in the input.
- A question must be a question — it may reference observed patterns as the subject of an
  interrogative, but must never assert a computed number as an established fact.
- Use plain business language, no SQL keywords or database terminology.

Respond with ONLY the JSON array — no markdown, no commentary.`

// profiler is the engine seam Profile needs, satisfied by engine.Handle.
type profiler interface {
	Query(ctx context.Context, sql string, args ...any) (engine.Rows, error)
}

// Profile runs cheap read-only aggregate queries over every SAFE column of every complete dataset
// in datasets, using the leased engine handle. Returns one SuggestionsProfile per safe column.
//
// Only safe columns are profiled: a PII-flagged or excluded column is skipped entirely (never even
// queried), matching FEAT-051's privacy rule. The queries are engine work (metered engine-seconds),
// never model calls.
func Profile(ctx context.Context, h profiler, datasets []domain.Dataset, columns []domain.Column) ([]model.SuggestionsProfile, error) {
	// Index safe columns by dataset for the per-view queries.
	type safeCol struct {
		col  domain.Column
		view string
	}
	var safe []safeCol
	complete := make(map[string]domain.Dataset)
	for _, d := range datasets {
		if d.IngestStatus == domain.IngestComplete {
			complete[d.ID] = d
		}
	}
	for _, c := range columns {
		if c.PIIFlag || c.ExcludedFromModel {
			continue
		}
		ds, ok := complete[c.DatasetID]
		if !ok {
			continue
		}
		safe = append(safe, safeCol{col: c, view: run.TableNameForDataset(ds)})
	}

	out := make([]model.SuggestionsProfile, 0, len(safe))
	for _, sc := range safe {
		p := model.SuggestionsProfile{Column: sc.col.Name, Type: string(sc.col.InferredType)}
		quoted := quoteIdent(sc.col.Name)
		switch sc.col.InferredType {
		case domain.TypeString, domain.TypeBoolean:
			rows, err := h.Query(ctx,
				`SELECT CAST(`+quoted+` AS VARCHAR) AS v, COUNT(*) AS c FROM `+sc.view+
					` WHERE `+quoted+` IS NOT NULL GROUP BY `+quoted+` ORDER BY c DESC LIMIT 5`)
			if err != nil {
				return nil, fmt.Errorf("profiling %s top values: %w", sc.col.Name, err)
			}
			for rows.Next() {
				var v string
				var c int64
				if err := rows.Scan(&v, &c); err != nil {
					rows.Close()
					return nil, fmt.Errorf("scanning %s top values: %w", sc.col.Name, err)
				}
				p.TopValues = append(p.TopValues, model.ValueCount{Value: v, Count: c})
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, fmt.Errorf("iterating %s top values: %w", sc.col.Name, err)
			}
			rows.Close()
		case domain.TypeInteger, domain.TypeNumber:
			rows, err := h.Query(ctx,
				`SELECT MIN(`+quoted+`), MAX(`+quoted+`), AVG(`+quoted+`) FROM `+sc.view)
			if err != nil {
				return nil, fmt.Errorf("profiling %s numeric range: %w", sc.col.Name, err)
			}
			if rows.Next() {
				var min, max, avg float64
				if err := rows.Scan(&min, &max, &avg); err != nil {
					rows.Close()
					return nil, fmt.Errorf("scanning %s numeric range: %w", sc.col.Name, err)
				}
				p.Numeric = &model.NumericRange{Min: min, Max: max, Avg: avg}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, fmt.Errorf("iterating %s numeric range: %w", sc.col.Name, err)
			}
			rows.Close()
		case domain.TypeDate, domain.TypeTimestamp:
			rows, err := h.Query(ctx,
				`SELECT CAST(`+quoted+` AS VARCHAR) AS d FROM `+sc.view+
					` WHERE `+quoted+` IS NOT NULL ORDER BY `+quoted+` DESC LIMIT 5`)
			if err != nil {
				return nil, fmt.Errorf("profiling %s top dates: %w", sc.col.Name, err)
			}
			for rows.Next() {
				var d string
				if err := rows.Scan(&d); err != nil {
					rows.Close()
					return nil, fmt.Errorf("scanning %s top dates: %w", sc.col.Name, err)
				}
				p.TopDates = append(p.TopDates, d)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, fmt.Errorf("iterating %s top dates: %w", sc.col.Name, err)
			}
			rows.Close()
		}
		out = append(out, p)
	}
	return out, nil
}

// ParseModelResponse parses FEAT-051's model output: a JSON array of suggestion strings. It
// tolerates markdown code fences the model sometimes wraps the JSON in (```json ... ```), and
// enforces the "never invent entities" rule by rejecting any suggestion that references no known
// safe entity (a safe column name or a defined metric name) — the same candidate-set discipline
// FEAT-050's automated test applies, now applied to the model's output.
func ParseModelResponse(text string, safeColumnNames []string, metricNames []string) ([]string, error) {
	var suggestions []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &suggestions); err != nil {
		// The model occasionally wraps the array in markdown fences; strip a ```json ... ```
		// (or ``` ... ```) block and retry before giving up.
		if fenced := stripCodeFence(text); fenced != "" {
			if err2 := json.Unmarshal([]byte(fenced), &suggestions); err2 == nil {
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("parsing smart suggestion response: %w", err)
		}
	}

	known := make(map[string]bool)
	for _, name := range safeColumnNames {
		known[normalizeEntity(name)] = true
	}
	for _, name := range metricNames {
		known[normalizeEntity(name)] = true
	}

	out := make([]string, 0, len(suggestions))
	seen := make(map[string]bool)
	for _, s := range suggestions {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		// A suggestion must reference at least one known safe entity; otherwise the model invented
		// a column/metric the user cannot actually ask about.
		referenced := false
		for entity := range known {
			if strings.Contains(normalizeEntity(s), entity) {
				referenced = true
				break
			}
		}
		if !referenced {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("smart suggestion response contained no usable questions")
	}
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out, nil
}

// stripCodeFence extracts the content of a ```lang ... ``` or ``` ... ``` fenced block, or returns
// "" when the text is not fenced. Fences are the model's most common JSON-wrapping habit; without
// this, a fenced response is unparseable even though its content is valid JSON.
func stripCodeFence(text string) string {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "```") {
		return ""
	}
	first := strings.Index(s, "\n")
	if first < 0 {
		return ""
	}
	last := strings.LastIndex(s, "```")
	if last <= first {
		return ""
	}
	return strings.TrimSpace(s[first+1 : last])
}

// normalizeEntity lowercases and collapses separators so entity matching is case/underscore-robust.
func normalizeEntity(s string) string {
	return strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(s))
}

// quoteIdent double-quotes a column name for use as a SQL identifier, doubling any embedded quote.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
