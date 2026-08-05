// Package semantic implements FEAT-019 (Semantic Layer Storage)'s domain logic — most importantly
// AssembleGroundingContext, the function that turns a workspace's confirmed relationships, metric
// definitions, and accumulated corrections into the structured grounding context FEAT-019's second
// acceptance criterion requires: "the semantic layer is used as grounding context for every
// subsequent question in the workspace."
//
// Nothing calls AssembleGroundingContext yet. FEAT-013 (Natural-Language Question -> SQL Generation)
// is the first feature to construct a model payload at all, and per its own description in
// features.md, that payload construction MUST go through one shared function — this package exists
// so FEAT-013 has a tested, ready-made building block to call into rather than reinventing this
// assembly step (or, worse, reading relationships/metrics/corrections directly and skipping it). No
// run, step, or model call is built here: that is Phase 2's next wave, not this one.
package semantic

import (
	"context"
	"fmt"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// RelationshipLister lists a workspace's confirmed relationships, satisfied by
// internal/store/postgres.RelationshipRepository.
type RelationshipLister interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Relationship, error)
}

// MetricDefinitionLister lists a workspace's metric definitions, satisfied by
// internal/store/postgres.MetricDefinitionRepository.
type MetricDefinitionLister interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error)
}

// CorrectionLister lists a workspace's persisted corrections, satisfied by
// internal/store/postgres.CorrectionRepository.
type CorrectionLister interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Correction, error)
}

// RelationshipGrounding is one confirmed join as presented to the model's query planner: column
// identity and grain, never row-level data. Field names are deliberately the same shape a
// generate_sql step would want for join planning (source/target column, cardinality, grain notes) —
// see architecture.md's payload-construction seam note.
type RelationshipGrounding struct {
	SourceColumnID string             `json:"source_column_id"`
	TargetColumnID string             `json:"target_column_id"`
	Cardinality    domain.Cardinality `json:"cardinality"`
	// GrainNotes is omitted entirely when empty, not sent as an empty string — a query planner should
	// never have to distinguish "no grain note" from "an intentionally blank one".
	GrainNotes string `json:"grain_notes,omitempty"`
}

// MetricGrounding is one named business metric as presented to the model's query planner — what a
// term like "revenue" means for this workspace, and the SQL expression that computes it.
type MetricGrounding struct {
	Name          string `json:"name"`
	SQLExpression string `json:"sql_expression"`
	Description   string `json:"description"`
}

// CorrectionGrounding is one accumulated correction as presented to the model's query planner. Only
// the corrected logic and why is included — never OriginalLogic — because the point of grounding on
// a correction is to bias the planner AWAY from repeating a mistake, not to hand it a worked example
// of the mistake itself.
type CorrectionGrounding struct {
	Description    string `json:"description"`
	CorrectedLogic string `json:"corrected_logic"`
}

// GroundingContext is a workspace's assembled semantic layer, ready to be embedded in a model
// payload by whichever step builds one.
//
// This shape is a considered but explicitly REVISABLE contract, not a schema.md-mandated wire
// format: no acceptance criterion in features.md prescribes the exact structure of "grounding
// context", only that the semantic layer must be used as one. FEAT-013 (Phase 2, next wave) is the
// first real consumer and is expected to adjust this shape freely — reshape, rename, or add fields —
// if its payload-construction function needs something different; nothing downstream of this wave
// depends on it staying exactly as written here.
type GroundingContext struct {
	WorkspaceID   string                  `json:"workspace_id"`
	Relationships []RelationshipGrounding `json:"relationships"`
	Metrics       []MetricGrounding       `json:"metrics"`
	Corrections   []CorrectionGrounding   `json:"corrections"`
}

// AssembleGroundingContext reads workspaceID's confirmed relationships, metric definitions, and
// corrections and assembles them into a GroundingContext.
//
// This is the FEAT-019 AC2 building block. It is deliberately NOT wired into any model call, run
// step, or HTTP route in this wave — see this package's doc comment. Calling it here, uncalled by
// anything else, is what lets FEAT-019 ship a real, tested contract for "grounding context" without
// building the query-planning machinery (FEAT-013) that consumes it.
func AssembleGroundingContext(
	ctx context.Context,
	workspaceID string,
	relationships RelationshipLister,
	metrics MetricDefinitionLister,
	corrections CorrectionLister,
) (GroundingContext, error) {
	rels, err := relationships.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return GroundingContext{}, fmt.Errorf("listing relationships for grounding context: %w", err)
	}
	metricRows, err := metrics.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return GroundingContext{}, fmt.Errorf("listing metric definitions for grounding context: %w", err)
	}
	correctionRows, err := corrections.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return GroundingContext{}, fmt.Errorf("listing corrections for grounding context: %w", err)
	}

	out := GroundingContext{
		WorkspaceID:   workspaceID,
		Relationships: make([]RelationshipGrounding, 0, len(rels)),
		Metrics:       make([]MetricGrounding, 0, len(metricRows)),
		Corrections:   make([]CorrectionGrounding, 0, len(correctionRows)),
	}
	for _, rel := range rels {
		out.Relationships = append(out.Relationships, RelationshipGrounding{
			SourceColumnID: rel.SourceColumnID,
			TargetColumnID: rel.TargetColumnID,
			Cardinality:    rel.Cardinality,
			GrainNotes:     rel.GrainNotes,
		})
	}
	for _, m := range metricRows {
		out.Metrics = append(out.Metrics, MetricGrounding{
			Name:          m.Name,
			SQLExpression: m.SQLExpression,
			Description:   m.Description,
		})
	}
	for _, c := range correctionRows {
		out.Corrections = append(out.Corrections, CorrectionGrounding{
			Description:    c.Description,
			CorrectedLogic: c.CorrectedLogic,
		})
	}
	return out, nil
}
