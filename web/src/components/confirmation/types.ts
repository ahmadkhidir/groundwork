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
  /**
   * When the owning dataset was uploaded, RFC 3339. Required for screens/07's hard grouping
   * requirement: two datasets sharing `dataset_filename` (FEAT-049's "Keep both" outcome) are
   * indistinguishable by filename alone — this is what disambiguates them in the sticky per-dataset
   * sub-header every Types/Sensitive-columns table must render.
   */
  dataset_created_at: string;
  name: string;
  inferred_type: InferredType;
  /**
   * Confidence 0-1, always describing `inferred_type` — the currently ACTIVE type (the user's
   * override once set, else the inferred type). It is the fit rate for that type read from
   * `type_fit_rates`, never a flat 1.0: confirming a type says what the user intended, not whether
   * the data supports it — only the second of those is worth a badge (FEAT-005/FEAT-006).
   */
  type_confidence: number;
  /**
   * 0-1 fit rate for every candidate type, keyed by the `InferredType` enum value — the basis of
   * the confidence badge. The Types step renders `type_fit_rates[override ?? inferred_type]`, so the
   * badge updates in the SAME interaction as an override with no re-query (FEAT-006, screens/07).
   * Optional: columns profiled before this was introduced, and older responses, omit it — treat a
   * missing key as "unknown" and fall back to `type_confidence` (mirrors the server's COALESCE).
   */
  type_fit_rates?: Partial<Record<InferredType, number>>;
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

/** One dataset named in a Relationship inference skipped notice, per FEAT-008/FEAT-049. */
export interface SkippedDatasetRef {
  id: string;
  created_at: string;
}

/**
 * One group of 2+ datasets sharing an original_filename, between which relationship inference was
 * never run — FEAT-008's unconditional same-filename exclusion, surfaced here rather than left as an
 * unexplained absence of candidates (the exact failure mode a real incident hit).
 */
export interface SkippedFilenamePair {
  filename: string;
  datasets: SkippedDatasetRef[];
}

export interface ListCandidateJoinsResponse {
  candidate_joins: ConfirmationCandidateJoin[];
  skipped_pairs?: SkippedFilenamePair[];
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

/**
 * The live fit rate for a column's ACTIVE type — the override if one is set, else the inferred
 * type. Reads `type_fit_rates` (measured once at profiling) so the badge describes whichever type
 * the user selects without a re-query; falls back to the stored `type_confidence` when the map
 * lacks the key, mirroring the server's COALESCE in `UpdateInferredType` for pre-`type_fit_rates`
 * columns.
 */
export function typeFitRate(
  column: Pick<ConfirmationColumn, "type_confidence" | "type_fit_rates">,
  type: InferredType,
): number {
  const rate = column.type_fit_rates?.[type];
  return typeof rate === "number" ? rate : column.type_confidence;
}

/** One dataset's columns, grouped for the sticky per-dataset sub-header on the Types/PII tables. */
export interface DatasetColumnGroup {
  datasetId: string;
  filename: string;
  createdAt: string;
  columns: ConfirmationColumn[];
}

/**
 * Groups columns by dataset_id, preserving each group's first-seen order — the hard requirement on
 * screens/07-ingest-confirmation.md's Types and Sensitive Columns tabs: "the table is always grouped
 * by source dataset, never a flat cross-dataset list of columns." Two datasets sharing a
 * dataset_filename (FEAT-049's "Keep both") are still two separate groups here, each labelled with
 * its own dataset_created_at, since that grouping is what a real incident's flat list lacked.
 */
export function groupColumnsByDataset(columns: ConfirmationColumn[]): DatasetColumnGroup[] {
  const groups = new Map<string, DatasetColumnGroup>();
  for (const col of columns) {
    let group = groups.get(col.dataset_id);
    if (!group) {
      group = {
        datasetId: col.dataset_id,
        filename: col.dataset_filename,
        createdAt: col.dataset_created_at,
        columns: [],
      };
      groups.set(col.dataset_id, group);
    }
    group.columns.push(col);
  }
  return Array.from(groups.values());
}

/** Renders a dataset group's sub-header label: "data.xlsx — uploaded Aug 5, 2:14 PM". */
export function datasetGroupLabel(group: Pick<DatasetColumnGroup, "filename" | "createdAt">): string {
  let uploaded = group.createdAt;
  try {
    uploaded = new Date(group.createdAt).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  } catch {
    // Fall back to the raw ISO string rather than throwing on an unparseable date.
  }
  return `${group.filename} — uploaded ${uploaded}`;
}

/** The three review steps, in order. "relationships" is included only when 2+ files were ingested. */
export const CONFIRMATION_STEPS = ["types", "pii", "relationships"] as const;
export type ConfirmationStep = (typeof CONFIRMATION_STEPS)[number];

export const STEP_LABELS: Record<ConfirmationStep, string> = {
  types: "Types",
  pii: "Sensitive columns",
  relationships: "Relationships",
};
