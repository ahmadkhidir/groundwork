// Package model is the ONLY route from anything in this service to the Anthropic model provider.
//
// This is the enforcement point risk_log.md R-016 exists to close and features.md FEAT-013 requires
// by name: "payload construction MUST read Column.pii_flag (and excluded_from_model, per FEAT-010)
// and reduce any flagged or excluded column to name-and-type only before it can reach the model —
// never its values. This MUST be implemented as one shared payload-construction function used by
// every model-calling step (plan, generate_sql, and later adversarial_review/narrate), not per-step
// ad hoc filtering, precisely so a future step can't forget to filter."
//
// The design makes bypassing that rule hard, not merely discouraged:
//
//  1. Payload's fields are UNEXPORTED. Nothing outside this package can construct a populated
//     Payload — there is no struct literal a caller in internal/run (or any future step package) can
//     write that produces one. The only ways to obtain a Payload are to call BuildPayload (metadata
//     only) or BuildFactsPayload (metadata plus the R-016-filtered execute result set) — both of
//     which are where the filtering rules live.
//  2. Client.Complete (client.go) accepts a Payload, not a []byte, string, or any other shape a
//     caller could hand-assemble and slip past BuildPayload. A future model-calling step (Phase 3's
//     adversarial_review, or a facts-consuming narrate/chart_spec) can only compile against this
//     seam by first calling one of the two constructors.
//  3. Metadata payloads never carry row-level data at all, filtered or not — per architecture.md's
//     egress rule, "schema/profile/aggregate metadata by default... never raw rows." ColumnMetadata
//     below has no field for a data value; there is nothing to accidentally forget to strip. Row
//     data exists only in the second constructor's Facts input, where BuildFactsPayload drops every
//     flagged/excluded column's values before the payload is sealed.
package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/semantic"
)

// ColumnMetadata is one column's profiling metadata, as read from domain.Column, before payload
// construction applies FEAT-007/FEAT-010's exposure rule.
type ColumnMetadata struct {
	Name              string
	Type              string
	PIIFlag           bool
	ExcludedFromModel bool
	// NullRate and DistinctCount are profiling AGGREGATES, never row values — but they are still
	// withheld for a flagged/excluded column per FEAT-013's "name-and-type only" rule, since even an
	// aggregate about a PII column's contents is more than "name and type".
	NullRate      float64
	DistinctCount int64
}

// TableMetadata groups a dataset's columns under the identifier generate_sql is asked to reference.
type TableMetadata struct {
	// DatasetID is carried for traceability in logs; never sent to the model.
	DatasetID string
	// Name is the sanitized SQL identifier the model must use to reference this table in generated
	// SQL — see internal/run's table-naming helper. Never the object-storage Parquet path.
	Name    string
	Columns []ColumnMetadata
}

// Facts is the executed result set FEAT-015 (narrate) and FEAT-016 (chart_spec) narrate/render
// against — the VALUES returned by the run's execute step, in contrast to TableMetadata, which
// carries only schema/profile aggregates.
//
// Facts is the second sealed payload input (alongside BuildFactsPayload below): it is the ONE shape
// that carries real row data toward the model, and it exists so architecture.md's "chart_spec +
// narrate consume facts only, not a raw context dump" rule has a typed conduit instead of an ad hoc
// channel. Rows MUST already be JSON scalars (string, float64, bool, or nil) — internal/run converts
// them at the execute boundary before constructing Facts.
type Facts struct {
	// Columns are the result column names, in order, one slot per element of every row.
	Columns []string
	// Rows is the executed result set, one []any per row, in Columns order. Every row must have
	// exactly len(Columns) elements.
	Rows [][]any
}

// ValueCount is one profiling bucket: a distinct value and how often it occurs.
type ValueCount struct {
	Value string
	Count int64
}

// NumericRange summarizes a numeric column's observed range for FEAT-051's smart generation.
type NumericRange struct {
	Min float64
	Max float64
	Avg float64
}

// SuggestionsProfile is on-demand profiling output for ONE safe column — the data-informed context
// FEAT-051 hands the model (top distinct values with counts, min/max/avg, top dates). Profiles are
// only ever built for safe columns (internal/suggestions filters before querying), and
// BuildSuggestionsPayload re-filters defensively: a PII-flagged or excluded column is excluded
// ENTIRELY — no name, no values — per FEAT-051's privacy acceptance criterion.
type SuggestionsProfile struct {
	Column    string
	Type      string
	TopValues []ValueCount
	Numeric   *NumericRange
	TopDates  []string
}

// payloadColumn is what actually reaches the model for one column: always name and type, plus the
// two profiling aggregates ONLY when the column is neither PII-flagged nor excluded.
type payloadColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// NullRate and DistinctCount are pointers so `omitempty`-equivalent omission is unambiguous: a
	// present 0.0 null rate must be distinguishable on the wire from "withheld because flagged".
	NullRate      *float64 `json:"null_rate,omitempty"`
	DistinctCount *int64   `json:"distinct_count,omitempty"`
}

// payloadTable is one table's filtered column list, as sent to the model.
type payloadTable struct {
	Name    string          `json:"name"`
	Columns []payloadColumn `json:"columns"`
}

// payloadRelationship mirrors semantic.RelationshipGrounding for the wire payload. Column identity
// and grain only — never row-level data, so no filtering is needed here.
type payloadRelationship struct {
	SourceColumnID string `json:"source_column_id"`
	TargetColumnID string `json:"target_column_id"`
	Cardinality    string `json:"cardinality"`
	GrainNotes     string `json:"grain_notes,omitempty"`
}

type payloadMetric struct {
	Name          string `json:"name"`
	SQLExpression string `json:"sql_expression"`
	Description   string `json:"description"`
}

type payloadCorrection struct {
	Description    string `json:"description"`
	CorrectedLogic string `json:"corrected_logic"`
}

// Payload is the ONLY shape that may reach Client.Complete. Every field is unexported; the sole
// constructors are BuildPayload (metadata only), BuildFactsPayload (metadata plus the execute
// result set, R-016-filtered), and BuildSuggestionsPayload (FEAT-051's smart-generation task). See
// this file's package doc for why that is load-bearing rather than a style preference.
type Payload struct {
	question      string
	priorContext  string
	tables        []payloadTable
	relationships []payloadRelationship
	metrics       []payloadMetric
	corrections   []payloadCorrection
	// facts is nil for metadata-only steps (plan, generate_sql); it is populated ONLY through
	// BuildFactsPayload, after the R-016 value filter has run. See BuildFactsPayload.
	facts *factSet
	// suggestions is nil for question-answering payloads; it is populated ONLY through
	// BuildSuggestionsPayload and marks the call as FEAT-051's smart-generation task rather than a
	// question-answering step.
	suggestions *wireSuggestionsTask
}

// factSet is the serialized form of Facts inside a Payload. Pointer so `omitempty` can drop it from
// metadata-only payloads entirely — a plan step must not carry a facts key at all.
type factSet struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// wireValueCount / wireNumericRange / wireSuggestionsProfile / wireSuggestionsTask are the wire
// shapes for FEAT-051's smart-generation task, kept unexported like the rest of the wire payload.
type wireValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type wireNumericRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

type wireSuggestionsProfile struct {
	Column    string            `json:"column"`
	Type      string            `json:"type"`
	TopValues []wireValueCount  `json:"top_values,omitempty"`
	Numeric   *wireNumericRange `json:"numeric,omitempty"`
	TopDates  []string          `json:"top_dates,omitempty"`
}

type wireSuggestionsTask struct {
	Candidates []string                 `json:"candidates"`
	Profiles   []wireSuggestionsProfile `json:"profiles"`
}

// wirePayload is Payload's exported mirror, used only internally by JSON to produce the exact bytes
// sent to the model and written to the audit/egress log. Never exported as a type itself — callers
// never get to hold one of these, only ask a Payload to serialize itself.
type wirePayload struct {
	Question string `json:"question"`
	// PriorContext carries a previous step's output (e.g. plan's approach) as grounding for a later
	// step (e.g. generate_sql). Empty for the first model-calling step in a run.
	PriorContext  string                `json:"prior_context,omitempty"`
	Tables        []payloadTable        `json:"tables"`
	Relationships []payloadRelationship `json:"relationships,omitempty"`
	Metrics       []payloadMetric       `json:"metrics,omitempty"`
	Corrections   []payloadCorrection   `json:"corrections,omitempty"`
	// Facts is present only when a step is built by BuildFactsPayload (narrate, chart_spec). Its
	// values have already been filtered by the R-016 exposure rule.
	Facts *factSet `json:"facts,omitempty"`
	// Suggestions is present only when the payload was built by BuildSuggestionsPayload (FEAT-051);
	// a question-answering payload never carries it.
	Suggestions *wireSuggestionsTask `json:"suggestions,omitempty"`
}

// BuildPayload is the single shared payload-construction function FEAT-013's description and R-016's
// mitigation require. Every model-calling step (this wave's plan and generate_sql; Phase 3/5's
// adversarial_review and narrate) MUST route through this function — see the package doc for how the
// type system makes that the only path rather than a convention.
//
// question is the user's natural-language question. priorContext is empty for the first model call
// in a run, or an earlier step's output (e.g. plan's chosen approach) for a later one — never raw
// row data. tables is every dataset's column metadata for the workspace; grounding is the workspace's
// assembled semantic layer from internal/semantic.AssembleGroundingContext.
//
// The exposure rule: any column with PIIFlag or ExcludedFromModel set is reduced to name and type
// only — NullRate and DistinctCount (and, a fortiori, any actual data value, which this shape never
// carries for any column) never reach the returned Payload for that column.
func BuildPayload(question, priorContext string, tables []TableMetadata, grounding semantic.GroundingContext) Payload {
	out := Payload{
		question:     question,
		priorContext: priorContext,
		tables:       make([]payloadTable, 0, len(tables)),
	}

	for _, t := range tables {
		pt := payloadTable{Name: t.Name, Columns: make([]payloadColumn, 0, len(t.Columns))}
		for _, c := range t.Columns {
			pc := payloadColumn{Name: c.Name, Type: c.Type}

			// The enforcement point: a flagged or excluded column stops here, at name+type. Nothing
			// below this branch ever runs for such a column.
			if !c.PIIFlag && !c.ExcludedFromModel {
				nullRate := c.NullRate
				distinctCount := c.DistinctCount
				pc.NullRate = &nullRate
				pc.DistinctCount = &distinctCount
			}
			pt.Columns = append(pt.Columns, pc)
		}
		out.tables = append(out.tables, pt)
	}

	out.relationships = make([]payloadRelationship, 0, len(grounding.Relationships))
	for _, rel := range grounding.Relationships {
		out.relationships = append(out.relationships, payloadRelationship{
			SourceColumnID: rel.SourceColumnID,
			TargetColumnID: rel.TargetColumnID,
			Cardinality:    string(rel.Cardinality),
			GrainNotes:     rel.GrainNotes,
		})
	}

	out.metrics = make([]payloadMetric, 0, len(grounding.Metrics))
	for _, m := range grounding.Metrics {
		out.metrics = append(out.metrics, payloadMetric{
			Name:          m.Name,
			SQLExpression: m.SQLExpression,
			Description:   m.Description,
		})
	}

	out.corrections = make([]payloadCorrection, 0, len(grounding.Corrections))
	for _, c := range grounding.Corrections {
		out.corrections = append(out.corrections, payloadCorrection{
			Description:    c.Description,
			CorrectedLogic: c.CorrectedLogic,
		})
	}

	return out
}

// BuildFactsPayload constructs the payload for a facts-consuming step — narrate and chart_spec,
// the two steps architecture.md's egress rule says consume "facts only" (never a raw context dump).
//
// It is built on top of the SAME metadata payload as BuildPayload (same tables, same R-016
// reduction of flagged/excluded columns to name-and-type in the metadata) PLUS the executed result
// set FEAT-015/FEAT-016 actually narrate against. facts carries row VALUES, which is exactly the
// category R-016 treats as the risk: any result column whose PIIFlag or ExcludedFromModel is set in
// the workspace metadata has its VALUES dropped from the facts before the payload is built — the
// column may still appear, name and type only, in the metadata (that is BuildPayload's job), but not
// a single value of it ever enters the returned payload. This keeps the "one shared construction
// function per step-type, no per-step ad hoc filtering" rule intact for the facts-consuming steps
// too: narrate/chart_spec must never assemble a facts payload by hand.
//
// facts.Rows must already be JSON scalars (internal/run converts them at the execute boundary).
func BuildFactsPayload(question, priorContext string, tables []TableMetadata, grounding semantic.GroundingContext, facts Facts) Payload {
	out := BuildPayload(question, priorContext, tables, grounding)

	// Build the set of column names whose VALUES must never leave: any column flagged PII or
	// excluded from the model in ANY known table.
	flag := make(map[int]bool) // facts.Columns index → drop
	flagged := flaggedColumnNames(tables)
	for i, col := range facts.Columns {
		if flagged[strings.ToLower(strings.TrimSpace(col))] {
			flag[i] = true
		}
	}

	// Reduce: drop the flagged columns' slots from both the column list and every row, preserving
	// order and row width consistency (every output row has exactly len(outColumns) elements).
	keptColumns := make([]string, 0, len(facts.Columns))
	for i, col := range facts.Columns {
		if !flag[i] {
			keptColumns = append(keptColumns, col)
		}
	}
	keptRows := make([][]any, 0, len(facts.Rows))
	for _, row := range facts.Rows {
		kept := make([]any, 0, len(keptColumns))
		for i, v := range row {
			if !flag[i] {
				kept = append(kept, v)
			}
		}
		keptRows = append(keptRows, kept)
	}

	out.facts = &factSet{Columns: keptColumns, Rows: keptRows}
	return out
}

// BuildSuggestionsPayload constructs the payload for FEAT-051's smart-question-generation model
// call — the same single shared construction seam as BuildPayload, but for a suggestions task.
//
// Privacy is STRICTER than question-answering: a column with PIIFlag or ExcludedFromModel is
// excluded from the suggestions payload ENTIRELY — no name, no type, no aggregates, no profiles —
// per FEAT-051's "never even by name" rule on the suggestions surface (the looser name-and-type
// reduction that BuildPayload applies is deliberately not used here). The builder re-filters the
// incoming profiles defensively even though internal/suggestions already filters before profiling.
//
// tables carry the same metadata FEAT-050's templates read (the R-016-filtered construction
// applies the name-and-type reduction in the metadata; the suggestions profiles carry the safe
// columns' on-demand profiling output). candidates is the deterministic suggestion set the model
// may rephrase and enrich — never inventing columns, metrics, or joins beyond it.
func BuildSuggestionsPayload(tables []TableMetadata, candidates []string, profiles []SuggestionsProfile) Payload {
	flagged := flaggedColumnNames(tables)

	// Defense in depth: drop any profile whose column is PII-flagged or excluded, even though the
	// profiler only queried safe columns.
	safeProfiles := make([]SuggestionsProfile, 0, len(profiles))
	for _, p := range profiles {
		if flagged[strings.ToLower(strings.TrimSpace(p.Column))] {
			continue
		}
		safeProfiles = append(safeProfiles, p)
	}

	wireProfiles := make([]wireSuggestionsProfile, 0, len(safeProfiles))
	for _, p := range safeProfiles {
		wp := wireSuggestionsProfile{Column: p.Column, Type: p.Type}
		for _, vc := range p.TopValues {
			wp.TopValues = append(wp.TopValues, wireValueCount{Value: vc.Value, Count: vc.Count})
		}
		if p.Numeric != nil {
			wp.Numeric = &wireNumericRange{Min: p.Numeric.Min, Max: p.Numeric.Max, Avg: p.Numeric.Avg}
		}
		wp.TopDates = append(wp.TopDates, p.TopDates...)
		wireProfiles = append(wireProfiles, wp)
	}

	base := BuildPayload("", "", tables, semantic.GroundingContext{})
	base.suggestions = &wireSuggestionsTask{Candidates: candidates, Profiles: wireProfiles}
	return base
}

// flaggedColumnNames returns the lowercased name of every column in tables whose values are
// withheld by FEAT-007 (PII) or FEAT-010 (excluded_from_model).
func flaggedColumnNames(tables []TableMetadata) map[string]bool {
	out := make(map[string]bool)
	for _, t := range tables {
		for _, c := range t.Columns {
			if c.PIIFlag || c.ExcludedFromModel {
				out[strings.ToLower(c.Name)] = true
			}
		}
	}
	return out
}

// JSON renders the exact bytes that will be (or were) sent to the model, as a string. This is what
// Client.Complete both transmits and writes verbatim to the audit/egress log via
// audit.Writer.WriteModelCall, in the same code path as the outbound call — never reconciled after
// the fact.
//
// A method rather than a free function so only a Payload obtained from BuildPayload can produce
// these bytes; there is no exported constructor for wirePayload itself.
func (p Payload) JSON() (string, error) {
	wire := wirePayload{
		Question:      p.question,
		PriorContext:  p.priorContext,
		Tables:        p.tables,
		Relationships: p.relationships,
		Metrics:       p.metrics,
		Corrections:   p.corrections,
		Facts:         p.facts,
		Suggestions:   p.suggestions,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("marshaling model payload: %w", err)
	}
	return string(body), nil
}
