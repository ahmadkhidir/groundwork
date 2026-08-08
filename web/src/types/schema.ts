/**
 * Groundwork domain types — the TypeScript mirror of docs/Groundwork/schema.md.
 *
 * This is the SINGLE definition of every entity shape on the client. Do not redeclare these shapes
 * in a component, and do not widen them with `any`. When schema.md changes, this file changes with
 * it and the Go equivalent in server/internal/domain moves in lockstep.
 *
 * Two conventions inherited directly from schema.md, both deliberate:
 *
 * 1. EMPTY STRING, NOT NULL. schema.md models "absent" as `""` for several string fields
 *    (`Run.parent_run_id`, `Step.output`, `Column.pii_pattern`, ...) rather than as null. The types
 *    below preserve that exactly — `parent_run_id: string` where `""` means "no parent" — because
 *    inventing `| null` here would put the client and the API contract out of step. Use the
 *    provided helpers (e.g. `isForkedRun`) rather than truthiness-checking these fields ad hoc.
 *
 * 2. IDS, NOT EMBEDDED OBJECTS. Every relationship is a `*_id` string. The only exceptions are
 *    `ChartSpec` and `ReconciliationSummary`, which are inlined value objects on `Report` because
 *    neither has independent identity outside its parent report.
 *
 * Raw ingested data is deliberately absent from this file: it lives as normalized Parquet in object
 * storage, referenced by `Dataset.parquet_location`. The DuckDB engine is a compute layer over it,
 * never a system of record.
 */

/* ------------------------------------------------------------------------------------------------
 * Enumerations
 *
 * Declared as const objects plus derived union types rather than TS `enum`, so they erase cleanly,
 * carry no runtime cost beyond the object, and remain usable as value lists for iteration.
 * ---------------------------------------------------------------------------------------------- */

/** Subscription tier. Gates usage volume, exports, sharing, and workspace count — never verification or privacy features. */
export const SubscriptionTier = {
  Free: "free",
  Pro: "pro",
  Team: "team",
} as const;
export type SubscriptionTier =
  (typeof SubscriptionTier)[keyof typeof SubscriptionTier];

/**
 * Live status of a workspace's session-scoped DuckDB engine.
 * Always shown honestly in the UI with a text label — never hidden behind a generic spinner.
 */
export const EngineStatus = {
  Sleeping: "sleeping",
  Waking: "waking",
  Ready: "ready",
} as const;
export type EngineStatus = (typeof EngineStatus)[keyof typeof EngineStatus];

/** Workspace access level. Only two roles exist: concurrent semantic-layer editing is out of scope. */
export const WorkspaceRole = {
  Owner: "owner",
  ReadOnly: "read_only",
} as const;
export type WorkspaceRole = (typeof WorkspaceRole)[keyof typeof WorkspaceRole];

/** Format of the originally uploaded file, kept for display and ingest routing. */
export const DatasetFormat = {
  Csv: "csv",
  Excel: "excel",
  Parquet: "parquet",
  Sqlite: "sqlite",
} as const;
export type DatasetFormat = (typeof DatasetFormat)[keyof typeof DatasetFormat];

/** Stage of the ingest pipeline, surfaced on the Ingest Progress screen. */
export const IngestStatus = {
  Pending: "pending",
  Provisioning: "provisioning",
  Profiling: "profiling",
  Complete: "complete",
  Failed: "failed",
} as const;
export type IngestStatus = (typeof IngestStatus)[keyof typeof IngestStatus];

/** Type inferred for a column at ingest. Reviewable and overridable by the user. */
export const InferredType = {
  String: "string",
  Integer: "integer",
  Number: "number",
  Boolean: "boolean",
  Date: "date",
  Timestamp: "timestamp",
} as const;
export type InferredType = (typeof InferredType)[keyof typeof InferredType];

/** Review state of an inferred join. Never auto-applied — a join is used only once accepted. */
export const CandidateJoinStatus = {
  Pending: "pending",
  Accepted: "accepted",
  Rejected: "rejected",
} as const;
export type CandidateJoinStatus =
  (typeof CandidateJoinStatus)[keyof typeof CandidateJoinStatus];

/** Cardinality of a confirmed relationship. Used by static SQL analysis to detect join-grain fan-out. */
export const Cardinality = {
  OneToOne: "one_to_one",
  OneToMany: "one_to_many",
  ManyToOne: "many_to_one",
  ManyToMany: "many_to_many",
} as const;
export type Cardinality = (typeof Cardinality)[keyof typeof Cardinality];

/** Overall status of a run, shown on the Run Viewer. */
export const RunStatus = {
  Queued: "queued",
  Waking: "waking",
  Running: "running",
  Halted: "halted",
  Failed: "failed",
  Complete: "complete",
} as const;
export type RunStatus = (typeof RunStatus)[keyof typeof RunStatus];

/**
 * The stages of the run DAG, in canonical pipeline order.
 *
 * Order matters: the Run Viewer renders the pipeline in this sequence, so this array is the display
 * order as well as the type source. Note `reconcile` and `shadow_check` sit between `execute` and
 * `narrate` — verification happens before the model ever writes prose about the numbers.
 */
export const STEP_NAMES = [
  "profile",
  "plan",
  "generate_sql",
  "static_check",
  "execute",
  "reconcile",
  "shadow_check",
  "adversarial_review",
  "chart_spec",
  "narrate",
  "assemble",
] as const;
export type StepName = (typeof STEP_NAMES)[number];

/** Status of a single step within a run. */
export const StepStatus = {
  Pending: "pending",
  Running: "running",
  Complete: "complete",
  Failed: "failed",
  Halted: "halted",
} as const;
export type StepStatus = (typeof StepStatus)[keyof typeof StepStatus];

/** Chart types the first-party renderer supports. Charts are specs, never images. */
export const ChartType = {
  Bar: "bar",
  Line: "line",
  Pie: "pie",
  Scatter: "scatter",
  Table: "table",
} as const;
export type ChartType = (typeof ChartType)[keyof typeof ChartType];

/** Category of a durable notification, kept separate from the high-frequency live run event stream. */
export const NotificationType = {
  ExportReady: "export_ready",
  EngineSlept: "engine_slept",
  IngestFailed: "ingest_failed",
  WorkspaceInvite: "workspace_invite",
} as const;
export type NotificationType =
  (typeof NotificationType)[keyof typeof NotificationType];

/** Entity kind a notification deep-links to. Resolves the polymorphic `related_entity_id`. */
export const RelatedEntityType = {
  Report: "report",
  Workspace: "workspace",
  Dataset: "dataset",
} as const;
export type RelatedEntityType =
  (typeof RelatedEntityType)[keyof typeof RelatedEntityType];

/** Category of action recorded in the combined audit + model-egress log. */
export const AuditActionType = {
  QueryExecuted: "query_executed",
  ModelCall: "model_call",
  Export: "export",
  Deletion: "deletion",
  Login: "login",
} as const;
export type AuditActionType =
  (typeof AuditActionType)[keyof typeof AuditActionType];

/** Categories of derived state a deletion cascade confirms destroyed. */
export const DeletedCategory = {
  ObjectStore: "object_store",
  SemanticLayer: "semantic_layer",
  ReportsAndConversation: "reports_and_conversation",
  QueryCaches: "query_caches",
  Logs: "logs",
} as const;
export type DeletedCategory =
  (typeof DeletedCategory)[keyof typeof DeletedCategory];

/**
 * An ISO 8601 date-time string as returned by the API.
 * Aliased for readability; the API never returns a Date instance, only a string.
 */
export type IsoDateTime = string;

/* ------------------------------------------------------------------------------------------------
 * Identity & access
 * ---------------------------------------------------------------------------------------------- */

/**
 * An account holder. Owns workspaces and is the identity behind every membership, run, report,
 * notification, and audit entry.
 */
export interface User {
  id: string;
  /** Used for authentication and notifications. */
  email: string;
  /** Shown in workspace member lists and audit entries. */
  display_name: string;
  /** Current subscription tier; determines feature gating across the product. */
  tier: SubscriptionTier;
  created_at: IsoDateTime;
}

/**
 * The durable container owning datasets, the semantic layer, runs, and reports.
 * This is the unit of both deletion and privacy.
 */
export interface Workspace {
  id: string;
  /** User-assigned workspace name. */
  name: string;
  /** References {@link User}. Each workspace has exactly one owner. */
  owner_id: string;
  /** Live engine status; always rendered honestly, never hidden. */
  engine_status: EngineStatus;
  created_at: IsoDateTime;
  /** Last modification — new dataset ingested, semantic layer changed, and so on. */
  updated_at: IsoDateTime;
}

/**
 * Grants a user access to a workspace beyond ownership.
 * Read-only members cannot mutate the semantic layer, upload data, or run queries — enforced at the
 * API layer, not merely hidden in the UI.
 */
export interface WorkspaceMember {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** References {@link User}. */
  user_id: string;
  role: WorkspaceRole;
  invited_at: IsoDateTime;
  /** When the invitation was accepted. Empty string if still pending. */
  accepted_at?: IsoDateTime;
}

/* ------------------------------------------------------------------------------------------------
 * Data & profiling
 * ---------------------------------------------------------------------------------------------- */

/**
 * One ingested and normalized file within a workspace.
 * The canonical stored form is normalized Parquet in object storage, not the original upload.
 */
export interface Dataset {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** The filename as uploaded, kept for display purposes only. */
  original_filename: string;
  format: DatasetFormat;
  /** URI of the normalized Parquet object — the canonical stored form. Absent until ingest completes. */
  parquet_location?: string;
  ingest_status: IngestStatus;
  /** Total rows in the normalized dataset. Absent until profiling completes. */
  row_count?: number;
  created_at: IsoDateTime;
}

/**
 * A single profiled column within a dataset, carrying inferred type, PII flag, and
 * model-visibility state.
 */
export interface Column {
  id: string;
  /** References {@link Dataset}. */
  dataset_id: string;
  /** Column name as it appears in the dataset. */
  name: string;
  /** Inferred at ingest; reviewable and overridable by the user. */
  inferred_type: InferredType;
  /** Confidence score 0-1 for the current active type — `type_fit_rates[active type]`, never a flat 1.0. */
  type_confidence: number;
  /** 0-1 fit rate for every candidate type; the confidence badge reads `type_fit_rates[active type]`. Optional: absent for columns profiled before this was introduced. */
  type_fit_rates?: Partial<Record<InferredType, number>>;
  /** Whether this column was auto-flagged as likely containing sensitive personal data. */
  pii_flag: boolean;
  /** Detected pattern when `pii_flag` is true — e.g. "email", "national_id". Empty string otherwise. */
  pii_pattern?: string;
  /**
   * When the user last set a type override on this column. Empty/absent when no override has ever
   * been set — the active type is still the original inference — distinguishing an overridden type
   * from the original profile (FEAT-048's deletion-impact disclosure reads this).
   */
  type_overridden_at?: string;
  /**
   * Whether the user has explicitly excluded this column from ever being sent to the model.
   * The column remains queryable by the engine regardless — exclusion is about egress, not access.
   */
  excluded_from_model: boolean;
  /** Fraction 0–1 of null values, from profiling. */
  null_rate: number;
  /** Number of distinct values, from profiling. */
  distinct_count: number;
}

/* ------------------------------------------------------------------------------------------------
 * Semantic layer
 * ---------------------------------------------------------------------------------------------- */

/**
 * A relationship between two columns proposed by inference at ingest, awaiting explicit user
 * confirmation. Never auto-applied.
 */
export interface CandidateJoin {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** References {@link Column}. May belong to a different dataset than the target. */
  source_column_id: string;
  /** References {@link Column}. */
  target_column_id: string;
  /** Confidence 0–1, from name similarity, value overlap, and cardinality checks. */
  confidence_score: number;
  /** Plain-language rationale, e.g. "matched column names, 94% value overlap". */
  basis: string;
  status: CandidateJoinStatus;
  created_at: IsoDateTime;
}

/**
 * A confirmed join persisted in the semantic layer, created when a {@link CandidateJoin} is accepted
 * or added manually.
 */
export interface Relationship {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** References {@link Column}. */
  source_column_id: string;
  /** References {@link Column}. */
  target_column_id: string;
  /** Used by static SQL analysis to detect join-grain fan-out. */
  cardinality: Cardinality;
  /** Free-text grain implications, e.g. "introduces one row per order line, not per order". */
  grain_notes?: string;
  confirmed_at: IsoDateTime;
}

/**
 * A named business metric definition — what "revenue" means for this workspace — used as grounding
 * when the model plans a query.
 */
export interface MetricDefinition {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** e.g. "revenue", "active_customer". */
  name: string;
  /** The SQL expression computing this metric. */
  sql_expression: string;
  /** Plain-language description of what it means and why it is defined this way. */
  description: string;
  created_at: IsoDateTime;
  updated_at: IsoDateTime;
}

/**
 * A persisted record of a user correcting generated SQL or semantic-layer logic during a run, so the
 * same mistake is not repeated.
 */
export interface Correction {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** Plain-language summary, e.g. "refunds join now excludes cancelled orders". */
  description: string;
  original_logic: string;
  corrected_logic: string;
  /** References the {@link Run} during which the correction was made. */
  originating_run_id: string;
  created_at: IsoDateTime;
}

/* ------------------------------------------------------------------------------------------------
 * Orchestration
 * ---------------------------------------------------------------------------------------------- */

/**
 * A single execution of the resumable DAG pipeline answering one question.
 * May fork from another run via `parent_run_id` when a completed step is amended.
 */
export interface Run {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** The natural-language question that initiated this run. */
  question_text: string;
  status: RunStatus;
  /** References another {@link Run} when forked from an amendment. Empty string if original. */
  parent_run_id?: string;
  created_at: IsoDateTime;
  /** When the run reached a terminal status. Empty string while in progress. */
  completed_at?: IsoDateTime;
}

/**
 * One node in a run's DAG, persisted at every boundary.
 *
 * This single entity is what makes interruption, amendment, provenance, and determinism caching all
 * fall out of one schema rather than four mechanisms.
 *
 * `input` and `output` are JSON-encoded STRINGS, not objects — deliberately. Payload shape genuinely
 * varies by `step_name` (generate_sql outputs SQL text; reconcile outputs row counts;
 * adversarial_review outputs a verdict). Typing per-step payloads is an implementation concern; see
 * the per-step payload types that accompany the orchestrator, and parse at the boundary.
 */
export interface Step {
  id: string;
  /** References {@link Run}. */
  run_id: string;
  /** References another {@link Step} when forked. Empty string if not forked. */
  parent_step_id?: string;
  step_name: StepName;
  status: StepStatus;
  /** JSON-encoded input. Steps are pure functions of this persisted input. */
  input?: string;
  /** JSON-encoded output. */
  output?: string;
  /** Guarantees a step cannot be double-executed on retry. */
  idempotency_key: string;
  /** Empty string if not yet started. */
  started_at?: IsoDateTime;
  /** Empty string while still in progress. */
  completed_at?: IsoDateTime;
}

/* ------------------------------------------------------------------------------------------------
 * Reporting & provenance
 * ---------------------------------------------------------------------------------------------- */

/**
 * A parsable chart specification rendered by first-party components.
 * Inlined on {@link Report} — it has no independent identity.
 */
export interface ChartSpec {
  chart_type: ChartType;
  /** Name of the result column mapped to the x-axis. */
  x_axis_column: string;
  /** Name of the result column mapped to the y-axis. */
  y_axis_column: string;
  /** Result column splitting the data into series, colored with the categorical chart tokens. Empty string for a single series. */
  series_column?: string;
  title: string;
}

/**
 * Row-level reconciliation data. Inlined on {@link Report} — no independent identity.
 *
 * `exclusion_warnings` is the honest-status principle in data form: if a filter dropped a
 * significant share of rows, the report says so unprompted rather than quietly reporting a number
 * computed over less data than the user assumes.
 */
export interface ReconciliationSummary {
  /** Total rows read before any filtering. */
  rows_in: number;
  /** Total rows remaining after all filtering and grouping. */
  rows_out: number;
  /** Plain-language warnings, e.g. "excluded 31% of rows where region was null". */
  exclusion_warnings: string[];
}

/**
 * A first-class, versioned answer object — not a chat message.
 * Fully self-contained and viewable without the session engine running.
 */
export interface Report {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** References the {@link Run} that produced this report. */
  run_id: string;
  title: string;
  /** Plain-language answer, generated only from the run's result set — never from data in context. */
  narrative: string;
  chart_spec: ChartSpec;
  /** The report's claim-level provenance, expanded into full link objects (FEAT-030). */
  provenance_links: ProvenanceLink[];
  reconciliation_summary: ReconciliationSummary;
  /** References another {@link Report} when forked by a compositional follow-up. Empty string if none. */
  parent_report_id?: string;
  created_at: IsoDateTime;
}

/**
 * Ties a specific number stated in a report's narrative to the exact query and rows that produced
 * it. This is the mechanism behind the product's core claim that every number is traceable.
 *
 * The report read endpoint expands each opaque id into one of these objects.
 */
export interface ProvenanceLink {
  id: string;
  /** References {@link Report}. */
  report_id: string;
  /** The exact text span in the narrative this link corresponds to. */
  claim_text: string;
  /** The value as stated, kept as a string to preserve exact formatting (currency, percentage). */
  stated_value: string;
  /** The exact SQL that produced this value. */
  source_query: string;
  /** The result column whose cell backs the figure — enables "relevant cell highlighted" (screens/10). */
  source_column: string;
  /** Identifiers of the specific result rows/cells backing the value. */
  source_row_ids: string[];
}

/** The backing result rows/cells for one claim (screens/10). `available=false` is the honest
 * "this source data is no longer available" state, distinct from a fetch error. */
export interface ProvenanceSource {
  available: boolean;
  columns?: string[];
  /** The specific backing rows, formatted as display text, in `columns` order. */
  rows?: string[][];
  /** The column whose cell should be highlighted. */
  highlight_column?: string;
}

/** GET /reports/:reportId/provenance/:claimId — the claim plus its reconstructed source rows. */
export interface ProvenanceClaimDetail {
  claim: ProvenanceLink & { run_id: string };
  source: ProvenanceSource;
}

/**
 * A read-only shareable link to a single report.
 * Access is scoped to possession of the token, not to account identity.
 */
export interface ReportShare {
  id: string;
  /** References {@link Report}. */
  report_id: string;
  /** The unguessable token used in the shareable URL. */
  share_token: string;
  revoked: boolean;
  created_at: IsoDateTime;
}

/* ------------------------------------------------------------------------------------------------
 * Operations — notifications, audit, usage, deletion
 * ---------------------------------------------------------------------------------------------- */

/**
 * A durable, persisted notification — deliberately separate from the high-frequency live run event
 * stream, which is transient.
 */
export interface Notification {
  id: string;
  /** References {@link User}. */
  user_id: string;
  type: NotificationType;
  message: string;
  /** Determines how `related_entity_id` is resolved into a deep link. */
  related_entity_type: RelatedEntityType;
  /** Id of a Report, Workspace, or Dataset, per `related_entity_type`. */
  related_entity_id: string;
  read: boolean;
  created_at: IsoDateTime;
}

/**
 * One entry in the COMBINED audit and model-egress log.
 *
 * This is deliberately one object rather than two: queries, model payloads, exports, and deletions
 * are recorded in one place so a user can answer "what left my workspace?" from a single view.
 */
export interface AuditLogEntry {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  /** References {@link User}. */
  user_id: string;
  action_type: AuditActionType;
  /** JSON-encoded payload sent to the model provider when `action_type` is `model_call`. Empty otherwise. */
  payload_sent_to_model?: string;
  timestamp: IsoDateTime;
}

/**
 * Metered usage for a workspace over a period, recorded from day one regardless of billing status.
 * Backs both the user-facing usage view and overage billing.
 */
export interface UsageRecord {
  id: string;
  /** References {@link Workspace}. */
  workspace_id: string;
  period_start: IsoDateTime;
  period_end: IsoDateTime;
  /** Engine compute time consumed in this period, in seconds. */
  engine_seconds: number;
  /** Object storage consumed by this workspace, in bytes, as of `period_end`. */
  storage_bytes: number;
  /** Model tokens (input plus output) consumed in this period. */
  model_tokens: number;
}

/**
 * Proof of a completed workspace deletion cascade.
 *
 * This is the SOLE surviving record of a workspace after deletion, retained specifically so that
 * deletion is provable to the user rather than merely asserted.
 */
export interface DeletionRecord {
  id: string;
  /** The id of the now-deleted workspace, retained as a historical reference only. */
  workspace_id: string;
  /** References the {@link User} who initiated the deletion. */
  initiated_by: string;
  /** Categories of derived state confirmed destroyed by this cascade. */
  deleted_categories: DeletedCategory[];
  completed_at: IsoDateTime;
}

/* ------------------------------------------------------------------------------------------------
 * Helpers for the empty-string-means-absent convention
 *
 * Use these instead of ad hoc truthiness checks, so the convention is applied consistently and a
 * future change to it has exactly one place to happen.
 * ---------------------------------------------------------------------------------------------- */

/** True when an optional schema string field carries a real value rather than the empty sentinel. */
export function isPresent(value: string | undefined | null): value is string {
  return typeof value === "string" && value.length > 0;
}

/** True when this run was forked from another via step amendment. */
export function isForkedRun(run: Run): boolean {
  return isPresent(run.parent_run_id);
}

/** True when this report was forked from a parent by a compositional follow-up question. */
export function isForkedReport(report: Report): boolean {
  return isPresent(report.parent_report_id);
}

/** True when a run has reached a terminal status and will not progress further. */
export function isTerminalRunStatus(status: RunStatus): boolean {
  return (
    status === RunStatus.Complete ||
    status === RunStatus.Failed ||
    status === RunStatus.Halted
  );
}
