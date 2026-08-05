// Package domain holds the entity types from docs/Groundwork/schema.md.
//
// These are the Go mirror of web/src/types/schema.ts; the two move together. JSON tags use the
// snake_case names from schema.md verbatim, so the wire format IS the documented schema and no
// translation layer is needed between the services.
//
// Two conventions inherited directly from schema.md:
//
//  1. Absent optional strings are the EMPTY STRING, not null. schema.md models "no parent run" as
//     "" rather than null, and these structs preserve that exactly. Fields carrying the convention
//     are tagged omitempty so an absent value stays absent on the wire.
//
//  2. Relationships are *_id strings, never embedded structs. The only exceptions are ChartSpec and
//     ReconciliationSummary, inlined into Report because neither has identity outside its report.
//
// Raw ingested data is deliberately absent: it lives as normalized Parquet in object storage,
// referenced by Dataset.ParquetLocation. The DuckDB engine is a compute layer over it, never a
// system of record.
package domain

import "time"

// SubscriptionTier is a user's billing tier. It gates usage volume, exports, sharing, and workspace
// count — never verification or privacy features, which are available on every tier.
type SubscriptionTier string

// Subscription tiers, per monetization.md.
const (
	TierFree SubscriptionTier = "free"
	TierPro  SubscriptionTier = "pro"
	TierTeam SubscriptionTier = "team"
)

// EngineStatus is the live state of a workspace's session-scoped DuckDB engine. It is always shown
// honestly in the UI with a text label, never hidden behind a generic spinner.
type EngineStatus string

// Engine statuses.
const (
	EngineSleeping EngineStatus = "sleeping"
	EngineWaking   EngineStatus = "waking"
	EngineReady    EngineStatus = "ready"
)

// WorkspaceRole is a member's access level. Only two roles exist because concurrent semantic-layer
// editing is explicitly out of scope.
type WorkspaceRole string

// Workspace roles.
const (
	RoleOwner    WorkspaceRole = "owner"
	RoleReadOnly WorkspaceRole = "read_only"
)

// DatasetFormat is the format of an originally uploaded file.
type DatasetFormat string

// Dataset formats accepted at upload.
const (
	FormatCSV     DatasetFormat = "csv"
	FormatExcel   DatasetFormat = "excel"
	FormatParquet DatasetFormat = "parquet"
	FormatSQLite  DatasetFormat = "sqlite"
)

// IngestStatus is the current stage of the ingest pipeline for one dataset.
type IngestStatus string

// Ingest statuses.
const (
	IngestPending      IngestStatus = "pending"
	IngestProvisioning IngestStatus = "provisioning"
	IngestProfiling    IngestStatus = "profiling"
	IngestComplete     IngestStatus = "complete"
	IngestFailed       IngestStatus = "failed"
)

// InferredType is the type inferred for a column at ingest. It is reviewable and overridable by the
// user before any query runs against it.
type InferredType string

// Inferred column types.
const (
	TypeString    InferredType = "string"
	TypeInteger   InferredType = "integer"
	TypeNumber    InferredType = "number"
	TypeBoolean   InferredType = "boolean"
	TypeDate      InferredType = "date"
	TypeTimestamp InferredType = "timestamp"
)

// CandidateJoinStatus is the review state of an inferred join. A join is never auto-applied; it is
// used only once explicitly accepted.
type CandidateJoinStatus string

// Candidate join statuses.
const (
	JoinPending  CandidateJoinStatus = "pending"
	JoinAccepted CandidateJoinStatus = "accepted"
	JoinRejected CandidateJoinStatus = "rejected"
)

// Cardinality describes a confirmed relationship's shape. Static SQL analysis uses it to detect
// join-grain fan-out before a query runs.
type Cardinality string

// Relationship cardinalities.
const (
	OneToOne   Cardinality = "one_to_one"
	OneToMany  Cardinality = "one_to_many"
	ManyToOne  Cardinality = "many_to_one"
	ManyToMany Cardinality = "many_to_many"
)

// RunStatus is a run's overall state.
type RunStatus string

// Run statuses.
const (
	RunQueued   RunStatus = "queued"
	RunWaking   RunStatus = "waking"
	RunRunning  RunStatus = "running"
	RunHalted   RunStatus = "halted"
	RunFailed   RunStatus = "failed"
	RunComplete RunStatus = "complete"
)

// IsTerminal reports whether the run has reached a state from which it will not progress on its own.
func (s RunStatus) IsTerminal() bool {
	return s == RunComplete || s == RunFailed || s == RunHalted
}

// StepName identifies a stage of the run DAG.
type StepName string

// Step names, per architecture.md's DAG.
const (
	StepProfile           StepName = "profile"
	StepPlan              StepName = "plan"
	StepGenerateSQL       StepName = "generate_sql"
	StepStaticCheck       StepName = "static_check"
	StepExecute           StepName = "execute"
	StepReconcile         StepName = "reconcile"
	StepShadowCheck       StepName = "shadow_check"
	StepAdversarialReview StepName = "adversarial_review"
	StepChartSpec         StepName = "chart_spec"
	StepNarrate           StepName = "narrate"
	StepAssemble          StepName = "assemble"
)

// PipelineOrder is the canonical execution order of the run DAG.
//
// Order is meaningful: reconcile and shadow_check sit between execute and narrate, so verification
// always happens before the model writes prose about the numbers. Never reorder this to "optimize"
// without changing architecture.md first.
var PipelineOrder = []StepName{
	StepProfile,
	StepPlan,
	StepGenerateSQL,
	StepStaticCheck,
	StepExecute,
	StepReconcile,
	StepShadowCheck,
	StepAdversarialReview,
	StepChartSpec,
	StepNarrate,
	StepAssemble,
}

// StepStatus is one step's state within a run.
type StepStatus string

// Step statuses.
const (
	StepPending  StepStatus = "pending"
	StepRunning  StepStatus = "running"
	StepComplete StepStatus = "complete"
	StepFailed   StepStatus = "failed"
	StepHalted   StepStatus = "halted"
)

// ChartType is a chart kind the first-party renderer supports. Charts are specs, never images.
type ChartType string

// Chart types.
const (
	ChartBar     ChartType = "bar"
	ChartLine    ChartType = "line"
	ChartPie     ChartType = "pie"
	ChartScatter ChartType = "scatter"
	ChartTable   ChartType = "table"
)

// NotificationType categorizes a durable notification. These are deliberately separate from the
// high-frequency live run event stream, which is transient and not persisted.
type NotificationType string

// Notification types.
const (
	NotifyExportReady     NotificationType = "export_ready"
	NotifyEngineSlept     NotificationType = "engine_slept"
	NotifyIngestFailed    NotificationType = "ingest_failed"
	NotifyWorkspaceInvite NotificationType = "workspace_invite"
)

// RelatedEntityType resolves a notification's polymorphic deep link target.
type RelatedEntityType string

// Related entity types.
const (
	RelatedReport    RelatedEntityType = "report"
	RelatedWorkspace RelatedEntityType = "workspace"
	RelatedDataset   RelatedEntityType = "dataset"
)

// AuditActionType categorizes an entry in the combined audit and model-egress log.
type AuditActionType string

// Audit action types.
const (
	ActionQueryExecuted AuditActionType = "query_executed"
	ActionModelCall     AuditActionType = "model_call"
	ActionExport        AuditActionType = "export"
	ActionDeletion      AuditActionType = "deletion"
	ActionLogin         AuditActionType = "login"
	// ActionPIIOverride records a user explicitly clearing or confirming a column's PII flag
	// (FEAT-007). Workspace-scoped. It has its own action type because it governs what the model is
	// allowed to see — a trust-surface decision, not internal bookkeeping.
	ActionPIIOverride AuditActionType = "pii_override"
)

// DeletedCategory is a category of derived state a deletion cascade confirms destroyed.
type DeletedCategory string

// Deleted categories.
const (
	CategoryObjectStore            DeletedCategory = "object_store"
	CategorySemanticLayer          DeletedCategory = "semantic_layer"
	CategoryReportsAndConversation DeletedCategory = "reports_and_conversation"
	CategoryQueryCaches            DeletedCategory = "query_caches"
	CategoryLogs                   DeletedCategory = "logs"
)

// AllDeletedCategories is every category a complete deletion cascade must cover.
//
// The deletion proof shown to the user is only meaningful if the cascade is exhaustive, so the
// cascade iterates this list rather than an ad hoc set written at the call site.
var AllDeletedCategories = []DeletedCategory{
	CategoryObjectStore,
	CategorySemanticLayer,
	CategoryReportsAndConversation,
	CategoryQueryCaches,
	CategoryLogs,
}

// User is an account holder. It owns workspaces and is the identity behind every membership, run,
// report, notification, and audit entry.
type User struct {
	ID          string           `json:"id"`
	Email       string           `json:"email"`
	DisplayName string           `json:"display_name"`
	Tier        SubscriptionTier `json:"tier"`
	CreatedAt   time.Time        `json:"created_at"`
}

// Workspace is the durable container owning datasets, the semantic layer, runs, and reports. It is
// the unit of both deletion and privacy.
type Workspace struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	OwnerID      string       `json:"owner_id"`
	EngineStatus EngineStatus `json:"engine_status"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// WorkspaceMember grants a user access to a workspace beyond ownership.
//
// Read-only members cannot mutate the semantic layer, upload data, or trigger runs. That is
// enforced at the API layer on every request, not merely hidden in the UI.
type WorkspaceMember struct {
	ID          string        `json:"id"`
	WorkspaceID string        `json:"workspace_id"`
	UserID      string        `json:"user_id"`
	Role        WorkspaceRole `json:"role"`
	InvitedAt   time.Time     `json:"invited_at"`
	// AcceptedAt is the zero time while the invitation is still pending.
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

// Dataset is one ingested and normalized file. The canonical stored form is normalized Parquet in
// object storage, not the original upload.
type Dataset struct {
	ID               string        `json:"id"`
	WorkspaceID      string        `json:"workspace_id"`
	OriginalFilename string        `json:"original_filename"`
	Format           DatasetFormat `json:"format"`
	// ParquetLocation is empty until ingest completes.
	ParquetLocation string       `json:"parquet_location,omitempty"`
	IngestStatus    IngestStatus `json:"ingest_status"`
	// RowCount is zero until profiling completes.
	RowCount  int64     `json:"row_count,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Column is a single profiled column, carrying inferred type, PII flag, and model-visibility state.
type Column struct {
	ID           string       `json:"id"`
	DatasetID    string       `json:"dataset_id"`
	Name         string       `json:"name"`
	InferredType InferredType `json:"inferred_type"`
	// TypeConfidence is in the range 0-1.
	TypeConfidence float64 `json:"type_confidence"`
	PIIFlag        bool    `json:"pii_flag"`
	// PIIPattern is the detected pattern when PIIFlag is set, e.g. "email". Empty otherwise.
	PIIPattern string `json:"pii_pattern,omitempty"`
	// PIIReviewedAt is when a user last made an explicit reviewed decision about PIIFlag — clearing
	// it, or confirming it as correct — as an RFC 3339 timestamp. Empty when no user has ever
	// reviewed this column, which is exactly what distinguishes a reviewed-and-cleared column from
	// one that was simply never flagged (FEAT-007).
	//
	// A string rather than *time.Time deliberately: schema.md models this field's absence as the
	// empty string, the same convention Column.PIIPattern and Run.ParentRunID already follow, and a
	// nil pointer would encode absence a second, conflicting way. The database column is nullable;
	// internal/store/postgres converts NULL <-> "" at the boundary, as migration 00002 established
	// for audit_log_entries.workspace_id.
	PIIReviewedAt string `json:"pii_reviewed_at,omitempty"`
	// ExcludedFromModel keeps this column out of every model payload. The engine may still query
	// it — exclusion governs egress, not access.
	ExcludedFromModel bool `json:"excluded_from_model"`
	// NullRate is the fraction 0-1 of null values, from profiling.
	NullRate      float64 `json:"null_rate"`
	DistinctCount int64   `json:"distinct_count"`
}

// CandidateJoin is a relationship between two columns proposed by inference at ingest, awaiting
// explicit user confirmation. It is never auto-applied.
type CandidateJoin struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	SourceColumnID string `json:"source_column_id"`
	TargetColumnID string `json:"target_column_id"`
	// ConfidenceScore is in the range 0-1, from name similarity, value overlap, and cardinality.
	ConfidenceScore float64 `json:"confidence_score"`
	// Basis is the plain-language rationale shown to the user, e.g. "matched column names, 94%
	// value overlap". A confidence number alone is not an explanation.
	Basis     string              `json:"basis"`
	Status    CandidateJoinStatus `json:"status"`
	CreatedAt time.Time           `json:"created_at"`
}

// Relationship is a confirmed join persisted in the semantic layer.
type Relationship struct {
	ID             string      `json:"id"`
	WorkspaceID    string      `json:"workspace_id"`
	SourceColumnID string      `json:"source_column_id"`
	TargetColumnID string      `json:"target_column_id"`
	Cardinality    Cardinality `json:"cardinality"`
	// GrainNotes records grain implications, e.g. "introduces one row per order line, not per order".
	GrainNotes  string    `json:"grain_notes,omitempty"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// MetricDefinition is a named business metric — what "revenue" means for this workspace — used as
// grounding when the model plans a query.
type MetricDefinition struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	Name          string    `json:"name"`
	SQLExpression string    `json:"sql_expression"`
	Description   string    `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Correction records a user correcting generated SQL or semantic-layer logic during a run, so the
// same mistake is not repeated on later runs.
type Correction struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	Description      string    `json:"description"`
	OriginalLogic    string    `json:"original_logic"`
	CorrectedLogic   string    `json:"corrected_logic"`
	OriginatingRunID string    `json:"originating_run_id"`
	CreatedAt        time.Time `json:"created_at"`
}

// Run is a single execution of the resumable DAG pipeline answering one question.
type Run struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	QuestionText string    `json:"question_text"`
	Status       RunStatus `json:"status"`
	// ParentRunID is set when this run was forked by amending a completed step. Empty if original.
	ParentRunID string    `json:"parent_run_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// CompletedAt is nil while the run is still in progress.
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Step is one node in a run's DAG, persisted at every boundary.
//
// This single entity is what makes interruption, amendment, provenance, and determinism caching all
// fall out of one schema rather than four separate mechanisms.
//
// Input and Output are JSON-encoded STRINGS by deliberate design, not oversight: payload shape
// genuinely varies by StepName (generate_sql outputs SQL text, reconcile outputs row counts,
// adversarial_review outputs a verdict). Typing them as one struct would be dishonestly narrow.
// Parse per StepName at the boundary.
type Step struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
	// ParentStepID captures forks within a run's amendment history. Empty if not forked.
	ParentStepID string     `json:"parent_step_id,omitempty"`
	StepName     StepName   `json:"step_name"`
	Status       StepStatus `json:"status"`
	Input        string     `json:"input,omitempty"`
	Output       string     `json:"output,omitempty"`
	// IdempotencyKey guarantees a step cannot be double-executed on retry.
	IdempotencyKey string     `json:"idempotency_key"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// ChartSpec is a parsable chart specification rendered by first-party components. It is inlined on
// Report because it has no identity outside its parent report.
type ChartSpec struct {
	ChartType    ChartType `json:"chart_type"`
	XAxisColumn  string    `json:"x_axis_column"`
	YAxisColumn  string    `json:"y_axis_column"`
	SeriesColumn string    `json:"series_column,omitempty"`
	Title        string    `json:"title"`
}

// ReconciliationSummary carries row-level reconciliation data. Inlined on Report; no independent
// identity.
//
// ExclusionWarnings is the honest-status principle in data form: if a filter dropped a significant
// share of rows, the report says so unprompted rather than quietly reporting a number computed over
// less data than the reader assumes.
type ReconciliationSummary struct {
	RowsIn            int64    `json:"rows_in"`
	RowsOut           int64    `json:"rows_out"`
	ExclusionWarnings []string `json:"exclusion_warnings"`
}

// Report is a first-class, versioned answer object — not a chat message. It is fully self-contained
// and viewable without the session engine running.
type Report struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
	Title       string `json:"title"`
	// Narrative is generated only from the run's result set, never from data held in model context.
	Narrative             string                `json:"narrative"`
	ChartSpec             ChartSpec             `json:"chart_spec"`
	ProvenanceLinks       []string              `json:"provenance_links"`
	ReconciliationSummary ReconciliationSummary `json:"reconciliation_summary"`
	// ParentReportID is set when a compositional follow-up forked this report. Empty otherwise.
	ParentReportID string    `json:"parent_report_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ProvenanceLink ties a specific number stated in a report's narrative to the exact query and rows
// that produced it. This is the mechanism behind the product's claim that every number is traceable.
type ProvenanceLink struct {
	ID       string `json:"id"`
	ReportID string `json:"report_id"`
	// ClaimText is the exact text span in the narrative this link corresponds to.
	ClaimText string `json:"claim_text"`
	// StatedValue is kept as a string to preserve exact display formatting (currency, percentage).
	StatedValue string `json:"stated_value"`
	SourceQuery string `json:"source_query"`
	// SourceRowIDs identifies the specific result rows backing the value.
	SourceRowIDs []string `json:"source_row_ids"`
}

// ReportShare is a read-only shareable link to a single report. Access is scoped to possession of
// the token, not to account identity.
type ReportShare struct {
	ID       string `json:"id"`
	ReportID string `json:"report_id"`
	// ShareToken must be generated with a cryptographically secure source: it is the only thing
	// standing between a URL and the report's contents.
	ShareToken string    `json:"share_token"`
	Revoked    bool      `json:"revoked"`
	CreatedAt  time.Time `json:"created_at"`
}

// Notification is a durable, persisted notification, separate from the transient live run event
// stream.
type Notification struct {
	ID                string            `json:"id"`
	UserID            string            `json:"user_id"`
	Type              NotificationType  `json:"type"`
	Message           string            `json:"message"`
	RelatedEntityType RelatedEntityType `json:"related_entity_type"`
	RelatedEntityID   string            `json:"related_entity_id"`
	Read              bool              `json:"read"`
	CreatedAt         time.Time         `json:"created_at"`
}

// AuditLogEntry is one entry in the COMBINED audit and model-egress log.
//
// Deliberately one object rather than two: queries, model payloads, exports, and deletions are
// recorded in one place so a user can answer "what left my workspace?" from a single view.
//
// PayloadSentToModel must be written in the SAME code path as the outbound model call, never
// reconciled afterwards — an egress log that can drift from actual egress is not evidence.
type AuditLogEntry struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	ActionType  AuditActionType `json:"action_type"`
	// PayloadSentToModel is populated only when ActionType is ActionModelCall.
	PayloadSentToModel string    `json:"payload_sent_to_model,omitempty"`
	Timestamp          time.Time `json:"timestamp"`
}

// UsageRecord is metered usage for a workspace over a period, recorded from day one regardless of
// billing status. It backs both the user-facing usage view and overage billing.
type UsageRecord struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	PeriodStart   time.Time `json:"period_start"`
	PeriodEnd     time.Time `json:"period_end"`
	EngineSeconds float64   `json:"engine_seconds"`
	StorageBytes  int64     `json:"storage_bytes"`
	ModelTokens   int64     `json:"model_tokens"`
}

// DeletionRecord is proof of a completed workspace deletion cascade.
//
// This is the SOLE surviving record of a workspace after deletion, retained specifically so that
// deletion is provable to the user rather than merely asserted.
type DeletionRecord struct {
	ID string `json:"id"`
	// WorkspaceID references a workspace that no longer exists; retained as a historical reference.
	WorkspaceID       string            `json:"workspace_id"`
	InitiatedBy       string            `json:"initiated_by"`
	DeletedCategories []DeletedCategory `json:"deleted_categories"`
	CompletedAt       time.Time         `json:"completed_at"`
}
