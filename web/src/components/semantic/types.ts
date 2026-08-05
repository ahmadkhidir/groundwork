import type { Cardinality } from "@/types/schema";

/**
 * A confirmed relationship as returned by GET /v1/workspaces/:id/semantic-layer/relationships.
 *
 * screens/15-semantic-layer.md's Relationships tab layout lists "source column, target column,
 * confidence, status (confirmed/rejected)" as its columns — but schema.md's Relationship entity
 * (which this type mirrors exactly) has no `confidence_score` or `status` field at all; those belong
 * to CandidateJoin, a different entity, per features.md FEAT-008/FEAT-009. A Relationship only ever
 * exists once confirmed (FEAT-009 creates it on accept), so "status" here is always, definitionally,
 * "confirmed" — there is no rejected state to show. Reported as a documentation inconsistency between
 * screens/15 and schema.md; this component renders what the Relationship entity actually carries
 * (cardinality, grain notes, confirmed_at) rather than inventing confidence/status fields.
 */
export interface SemanticRelationship {
  id: string;
  workspace_id: string;
  source_column_id: string;
  target_column_id: string;
  cardinality: Cardinality;
  grain_notes?: string;
  confirmed_at: string;
}

/** A named business metric definition, as returned by the semantic layer API. */
export interface SemanticMetricDefinition {
  id: string;
  workspace_id: string;
  name: string;
  sql_expression: string;
  description: string;
  created_at: string;
  updated_at: string;
}

/**
 * A persisted correction, as returned by GET /v1/workspaces/:id/semantic-layer/corrections.
 *
 * `originating_run_id` is absent/empty when the originating run has since been deleted (e.g.
 * retention pruning) — screens/15's documented edge case. Use {@link isPresent} rather than a
 * truthiness check, matching the empty-string-means-absent convention used elsewhere (e.g.
 * ConfirmationColumn.pii_reviewed_at).
 */
export interface SemanticCorrection {
  id: string;
  workspace_id: string;
  description: string;
  original_logic: string;
  corrected_logic: string;
  originating_run_id?: string;
  created_at: string;
}

export interface ListRelationshipsResponse {
  relationships: SemanticRelationship[];
}
export interface ListMetricDefinitionsResponse {
  metric_definitions: SemanticMetricDefinition[];
}
export interface ListCorrectionsResponse {
  corrections: SemanticCorrection[];
}
export interface UpdateRelationshipResponse {
  relationship: SemanticRelationship;
}
export interface MetricDefinitionResponse {
  metric_definition: SemanticMetricDefinition;
}
export interface SemanticSummaryResponse {
  relationship_count: number;
  metric_definition_count: number;
}

/** True when a schema.md empty-string-means-absent field is actually present. */
export function isPresent(value: string | undefined): value is string {
  return Boolean(value);
}

/** The three tabs, in screens/15-semantic-layer.md's documented order. */
export const SEMANTIC_TABS = ["relationships", "metrics", "corrections"] as const;
export type SemanticTab = (typeof SEMANTIC_TABS)[number];

export const SEMANTIC_TAB_LABELS: Record<SemanticTab, string> = {
  relationships: "Relationships",
  metrics: "Metric Definitions",
  corrections: "Corrections",
};

/** Options for the cardinality select in the Relationships tab's Edit form. */
export const CARDINALITY_OPTIONS: ReadonlyArray<{ value: Cardinality; label: string }> = [
  { value: "one_to_one", label: "One to one" },
  { value: "one_to_many", label: "One to many" },
  { value: "many_to_one", label: "Many to one" },
  { value: "many_to_many", label: "Many to many" },
];
