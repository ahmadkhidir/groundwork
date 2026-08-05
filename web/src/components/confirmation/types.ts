import type { CandidateJoinStatus, InferredType } from "@/types/schema";

/**
 * A profiled column as returned by GET /v1/workspaces/:id/columns — the workspace-wide superset of
 * `@/types/schema`'s `Column`, labelled with its owning file's name since screens/07 reviews an
 * entire ingest batch (potentially several files) as one screen.
 */
export interface ConfirmationColumn {
  id: string;
  dataset_id: string;
  dataset_filename: string;
  name: string;
  inferred_type: InferredType;
  /** Confidence 0-1. Set to 1.0 the moment a human overrides the type or PII flag. */
  type_confidence: number;
  pii_flag: boolean;
  /** Detected (or "manual") pattern when pii_flag is true. Empty string otherwise. */
  pii_pattern?: string;
  /**
   * When a user last made an explicit decision about `pii_flag` — clearing it, or confirming it.
   * Empty/absent when nobody ever has, which is exactly what distinguishes a column reviewed and
   * cleared from one that was simply never flagged (FEAT-007). Use {@link isPresent} rather than a
   * truthiness check, per the empty-string-means-absent convention.
   */
  pii_reviewed_at?: string;
  excluded_from_model: boolean;
  null_rate: number;
  distinct_count: number;
}

/** A candidate join as returned by GET /v1/workspaces/:id/candidate-joins. */
export interface ConfirmationCandidateJoin {
  id: string;
  source_column_id: string;
  target_column_id: string;
  confidence_score: number;
  basis: string;
  status: CandidateJoinStatus;
  created_at: string;
}

/** A confirmed relationship, returned when a candidate join is accepted. */
export interface ConfirmationRelationship {
  id: string;
  workspace_id: string;
  source_column_id: string;
  target_column_id: string;
  cardinality: string;
  grain_notes?: string;
  confirmed_at: string;
}

export interface ListColumnsResponse {
  columns: ConfirmationColumn[];
}

export interface ListCandidateJoinsResponse {
  candidate_joins: ConfirmationCandidateJoin[];
}

export interface UpdateColumnResponse {
  column: ConfirmationColumn;
}

export interface UpdateCandidateJoinResponse {
  candidate_join: ConfirmationCandidateJoin;
  relationship?: ConfirmationRelationship;
}

/**
 * Confidence tier — high/medium/low — always paired with the numeric percent or basis text per
 * branding.md's Color independence rule ("status is never conveyed by color alone").
 */
export type ConfidenceTier = "high" | "medium" | "low";

/** Thresholds mirroring the confidence bands a user can reason about at a glance. */
export function confidenceTier(score: number): ConfidenceTier {
  if (score >= 0.85) return "high";
  if (score >= 0.6) return "medium";
  return "low";
}

/** The three review steps, in order. "relationships" is included only when 2+ files were ingested. */
export const CONFIRMATION_STEPS = ["types", "pii", "relationships"] as const;
export type ConfirmationStep = (typeof CONFIRMATION_STEPS)[number];

export const STEP_LABELS: Record<ConfirmationStep, string> = {
  types: "Types",
  pii: "Sensitive columns",
  relationships: "Relationships",
};
