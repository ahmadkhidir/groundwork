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
//     write that produces one. The only way to obtain a Payload is to call BuildPayload, which is
//     where the filtering rule lives.
//  2. Client.Complete (client.go) accepts a Payload, not a []byte, string, or any other shape a
//     caller could hand-assemble and slip past BuildPayload. A future model-calling step (Phase 3's
//     adversarial_review, narrate) can only compile against this seam by first calling BuildPayload.
//  3. Payload never carries row-level data at all, filtered or not — per architecture.md's egress
//     rule, "schema/profile/aggregate metadata by default... never raw rows." ColumnMetadata below
//     has no field for a data value; there is nothing to accidentally forget to strip.
package model

import (
	"encoding/json"
	"fmt"

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
// constructor is BuildPayload. See this file's package doc for why that is load-bearing rather than
// a style preference.
type Payload struct {
	question      string
	priorContext  string
	tables        []payloadTable
	relationships []payloadRelationship
	metrics       []payloadMetric
	corrections   []payloadCorrection
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
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("marshaling model payload: %w", err)
	}
	return string(body), nil
}
