// Package api holds the HTTP layer: routing, middleware, and response shaping.
//
// This service is the authorization boundary. The Next.js tier's proxy check is an optimistic
// redirect for the user's benefit; every handler here re-verifies identity and workspace scope
// regardless of what the client asserts.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
	"github.com/ahmadkhidir/groundwork/server/internal/ingest"
)

// Dependencies bundles every wave-2-and-later collaborator NewServer needs, beyond auth. Grouped
// into a struct (rather than growing NewServer's positional parameter list every wave) so a later
// wave adding, say, the orchestrator or engine provider doesn't force every existing call site to
// change.
type Dependencies struct {
	// AuthSecret decodes the Auth.js session cookie the browser sends directly to this service — see
	// internal/authsession's package doc for why that is the actual authorization boundary. Supplied
	// from config.Config.AuthSecret, which owns it as of Wave 2.
	AuthSecret string

	Workspaces WorkspaceStore
	Users      UserTierLookup
	Datasets   DatasetStore
	Objects    ObjectStore
	AuditLog   AuditStore

	// IngestDatasets, IngestPipeline, and IngestTracker back FEAT-004's endpoints. Datasets above
	// (FEAT-003's narrower Create-only seam) is left untouched; this is an additive, wider seam over
	// the same underlying DatasetRepository, satisfied by IngestDatasetStore/IngestPipeline in
	// ingest.go.
	IngestDatasets IngestDatasetStore
	IngestPipeline IngestPipeline
	IngestTracker  *ingest.Tracker

	// ProfilingDatasets, Columns, and CandidateJoins back FEAT-005/FEAT-008's read-only endpoints.
	// Profiling statistics are stored at ingest and served from here, never recomputed per request —
	// an explicit FEAT-005 acceptance criterion.
	//
	// ProfilingDatasets is its own field rather than reusing Datasets or IngestDatasets: those are
	// FEAT-003's Create-only and FEAT-004's list-only seams respectively, and neither exposes
	// GetByID. All three are satisfied by the same DatasetRepository; keeping the interfaces narrow
	// is what lets each handler's test double stay small.
	ProfilingDatasets ProfilingDatasetStore
	Columns           ProfilingColumnStore
	CandidateJoins    ProfilingCandidateJoinStore

	// ConfirmationDatasets, ConfirmationColumns, ConfirmationCandidateJoins, and Relationships back
	// screens/07-ingest-confirmation.md's write endpoints — FEAT-006 (type override), FEAT-007 (PII
	// flag override), and FEAT-009 (candidate join accept/reject). Distinct fields from
	// ProfilingDatasets/Columns/CandidateJoins above for the same reason those are distinct from
	// Datasets/IngestDatasets: each interface here is intentionally narrower than the concrete
	// repository (e.g. ConfirmationColumnStore adds UpdateInferredType/UpdatePII that
	// ProfilingColumnStore has no reason to expose), even though one ColumnRepository,
	// DatasetRepository, and CandidateJoinRepository instance satisfies all of them.
	ConfirmationDatasets       ConfirmationDatasetStore
	ConfirmationColumns        ConfirmationColumnStore
	ConfirmationCandidateJoins ConfirmationCandidateJoinStore
	Relationships              ConfirmationRelationshipStore

	// ConfirmationAudit records FEAT-007's PII override as a `pii_override` audit entry, written in
	// the same handler that writes the override itself. Satisfied by internal/audit.Writer, the same
	// instance behind AuditLog's read side.
	ConfirmationAudit ConfirmationAuditWriter

	// EngineProvider backs FEAT-011's honest engine status. Deliberately typed as the narrow
	// EngineStatusProvider rather than *engine.LocalProvider: the handler only ever reads Status, and
	// architecture.md's Engine Provider seam exists so a later non-DuckDB implementation can be
	// swapped in without touching callers. Status must never provoke a wake — see engine.Provider.
	EngineProvider EngineStatusProvider

	// EngineDatasets backs the has_datasets field screens/04-workspace-home.md requires on the
	// single-workspace GET. Same DatasetRepository as the seams above, narrowed to one existence
	// question.
	EngineDatasets WorkspaceDatasetPresence

	// SemanticRelationships, SemanticMetrics, SemanticCorrections, and SemanticSummary back FEAT-019
	// (Semantic Layer Storage) — screens/15-semantic-layer.md's three tabs and
	// screens/04-workspace-home.md's summary panel. SemanticRelationships is a wider seam over the
	// SAME relationships table Relationships (above) writes into on candidate-join accept; see
	// semantic.go's doc comment for why the read/edit surface extends that repository rather than
	// duplicating it.
	SemanticRelationships SemanticRelationshipStore
	SemanticMetrics       SemanticMetricDefinitionStore
	SemanticCorrections   SemanticCorrectionStore
	SemanticSummary       SemanticSummaryStore

	// Suggestions backs FEAT-050's deterministic question-suggestions strip (screens/04-workspace-
	// home.md): the same four listers suggestions.Generate reads, satisfied by the postgres
	// repositories. The UI hides the strip for read-only members, but the endpoint is a harmless
	// metadata read behind the ordinary read boundary.
	Suggestions SuggestionSource
	// SuggestionEngine + SuggestionModel back FEAT-051's smart generation (the same engine Provider
	// and model Client the run executor uses). When either is nil the smart route is not registered
	// and only the deterministic strip exists.
	SuggestionEngine SuggestionEngineProvider
	SuggestionModel  SmartSuggestionModel
	// SuggestionObjects downloads a workspace's normalized Parquet so the smart-generation handler
	// can attach the engine views it profiles (FEAT-051). Satisfied by storage.Client.
	SuggestionObjects ObjectGetter
	// SuggestionBucket persists the workspace's model-generated smart suggestions (FEAT-051's
	// "add to the list" behavior): the GET returns them alongside the deterministic strip, smart
	// generation appends to them, and DELETE removes one. Satisfied by
	// postgres.SuggestionQuestionRepository.
	SuggestionBucket SuggestionBucket

	// Datasets... below back FEAT-048 (Single-Dataset Deletion) — screens/04-workspace-home.md's
	// Datasets panel and the shared Delete Confirmation Modal's data source — and FEAT-049's
	// duplicate-filename check (DuplicateCheck), which also calls FEAT-048's deletion directly for its
	// Replace path. See datasets.go and upload.go's DeleteDatasetAndObjects/handleCheckFilename.
	DatasetsList         DatasetsStore
	DatasetColumns       DatasetColumnsStore
	DatasetRelationships DatasetRelationshipsStore
	DatasetObjects       DatasetObjectDeleter
	DatasetDeletionAudit DatasetDeletionAuditWriter
	DuplicateCheck       DuplicateCheckStore
	// DatasetStorageReconciler refreshes a workspace's storage_bytes metering snapshot after a
	// dataset delete (billing milestone, pass 1 — best-effort). Satisfied by
	// metering.NewStorageReconciler.
	DatasetStorageReconciler DatasetStorageReconciler

	// Runs, RunSteps, and RunExecutor back FEAT-013 (Natural-Language Question → SQL Generation) —
	// screens/04-workspace-home.md's Ask panel and screens/08-run-viewer.md's live run. Runs is the
	// create/get seam over the runs table; RunSteps exposes a run's persisted step feed to the
	// viewer; RunExecutor starts the background linear executor (internal/run.Executor) — the only
	// thing allowed to create step rows or advance run status after the handler's queued insert.
	Runs        RunStore
	RunSteps    RunStepStore
	RunExecutor RunExecutor
	// RunEvents backs FEAT-023's live run-event stream. When non-nil it is the in-process fan-out the
	// executor publishes to (WithEvents) and the stream endpoint subscribes to; the executor + stream
	// are only wired together when this is set in main.go.
	RunEvents *events.Hub

	// Reports backs FEAT-014's report read endpoint — screens/09-report-view.md's single self-
	// contained fetch. Satisfied by postgres.ReportRepository, the same instance the executor uses
	// to materialize reports on run completion.
	Reports ReportStore
	// Provenance backs FEAT-030's claim-level provenance — the report read endpoint expands a
	// report's provenance_links ids into claim objects, and the provenance-panel detail endpoint
	// (screens/10) serves a single claim's query and backing result rows. Satisfied by
	// postgres.ProvenanceRepository, the same instance the executor wires via run.WithProvenance.
	Provenance ProvenanceStore

	// ExportAudit records FEAT-035/FEAT-036's report exports as `export` audit entries (FEAT-042),
	// written in the same handler that serves the export bytes — screens/11-export-dialog.md.
	// Satisfied by internal/audit.Writer, the same instance behind ConfirmationAudit.
	ExportAudit ExportAuditWriter

	// FEAT-043 (One-Workspace-One-Delete Cascade) — screens/14-deletion-confirmation-proof.md.
	// WorkspaceCascadeObjects deletes every object under the workspace's key prefix (satisfied by
	// internal/storage.Client); WorkspaceEngineDropper tears down the workspace's session-scoped
	// engine state (satisfied by engine.Provider once it implements Drop — nil until then skips
	// engine teardown); WorkspaceDeletionCascade runs the database half of the cascade (satisfied
	// by postgres.WorkspaceRepository's delete methods); WorkspaceDeletionProofs reads the
	// surviving proof record (satisfied by postgres.DeletionRecordRepository);
	// WorkspaceDeletionAudit writes the cascade's own audit entry before the log rows are destroyed
	// (satisfied by internal/audit.Writer).
	WorkspaceCascadeObjects  WorkspaceCascadeObjectDeleter
	WorkspaceEngineDropper   WorkspaceEngineDropper
	WorkspaceDeletionCascade WorkspaceDeletionStore
	WorkspaceDeletionProofs  DeletionProofStore
	WorkspaceDeletionAudit   WorkspaceDeletionAuditWriter

	// FEAT-045 (Read-Only Workspace Sharing) — screens/13-workspace-settings.md. Members manages
	// the workspace_members table (satisfied by postgres.MemberRepository); MemberLookup is the same
	// repository narrowed to the access-check read (owner OR member) every handler uses; MemberUsers
	// resolves an invite target by email (satisfied by postgres.UserRepository.FindByEmail — the
	// invite flow needs the users table, and only the user store may answer "does this email exist?").
	Members      MemberStore
	MemberLookup MemberLookupStore
	MemberUsers  UserByEmailStore

	// Shares backs FEAT-046 (Read-Only Report Sharing by ID): the owner-side create/list/revoke
	// endpoints and the UNAUTHENTICATED public read resolved by token possession. Satisfied by
	// postgres.ReportShareRepository. ShareLookup is the same repository narrowed to the public
	// token-resolving read only — the unauthenticated handler must not be able to create or revoke
	// anything.
	Shares      ReportShareStore
	ShareLookup ShareLookupStore

	// Usage backs FEAT-047 (User-Facing Usage View) — the Usage tab on Workspace Settings.
	// Satisfied by postgres.UsageRepository (read-only over usage_records).
	Usage UsageStore

	// EngineWake backs FEAT-012 (Wake-on-Intent Trigger): a POST endpoint that acquires then
	// releases the workspace engine so focusing the question input can pre-wake it. Satisfied by
	// engine.Provider (the same instance behind EngineProvider).
	EngineWake EngineWakeProvider

	// Notifications backs FEAT-026 (Durable Notifications) — screens/16-notifications.md.
	// Satisfied by postgres.NotificationRepository. The API read is tier-gated (Pro/Team, per
	// monetization.md); the event sources that CREATE notifications live in other packages and are
	// wired in assembly.
	Notifications NotificationStore
	// ExportNotifications is the narrow Create-only seam the export handler (FEAT-035..040) uses to
	// write `export_ready` notifications, satisfied by the same postgres.NotificationRepository.
	// Deliberately separate from Notifications so a read/acknowledge-only store can never be
	// confused with the event-source write.
	ExportNotifications NotificationCreator

	// Billing (billing milestone, pass 2 — Stripe integration). BillingUsers is the ONLY seam
	// allowed to change users.tier (satisfied by postgres.UserRepository); BillingEvents records
	// Stripe webhook events durably and dedupes by event id (satisfied by
	// postgres.BillingEventRepository); BillingClient wraps the Stripe SDK (satisfied by
	// *billing.Client).
	BillingUsers  BillingUserStore
	BillingEvents BillingEventStore
	BillingClient StripeClient
}

// Server wires configuration and dependencies into an http.Handler.
type Server struct {
	cfg    config.Config
	logger *slog.Logger
	router chi.Router

	authService *auth.Service
	deps        Dependencies
}

// NewServer constructs a Server with all routes registered.
//
// authService may be nil only in tests that don't exercise auth-backed routes; production callers
// (cmd/api/main.go) always supply one. deps' fields are likewise only required for the routes that
// use them — tests exercising just /healthz or /v1/auth/* can pass a zero-value Dependencies.
func NewServer(cfg config.Config, logger *slog.Logger, authService *auth.Service, deps Dependencies) *Server {
	s := &Server{
		cfg:         cfg,
		logger:      logger,
		router:      chi.NewRouter(),
		authService: authService,
		deps:        deps,
	}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// routes registers middleware and every endpoint.
//
// Middleware order is significant: RequestID must come first so every later log line and error
// response can carry the correlation id, and Recoverer must wrap the handlers so a panic becomes a
// 500 rather than killing the process.
func (s *Server) routes() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(s.requestLogger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.Timeout(60 * time.Second))
	s.router.Use(s.cors)

	// Liveness and readiness. Deliberately unauthenticated: a health check that requires a session
	// cannot be used by a load balancer.
	s.router.Get("/healthz", s.handleHealth)

	s.router.Route("/v1", func(r chi.Router) {
		// FEAT-001: signup, login (credential verification for the Next.js Credentials provider —
		// the web layer never reads the users table itself, per architecture.md's boundary rule),
		// and password reset.
		if s.authService != nil {
			RegisterAuthRoutes(r, s.authService, s.logger)
		}

		// FEAT-046's UNAUTHENTICATED shared-report read: GET /v1/shared/reports/:shareToken.
		// Deliberately registered on the public /v1 group, OUTSIDE authsession.RequireAuth — access
		// is scoped to possession of the share token, not account identity (user_flow.md's
		// Authentication Gate). The handler only reads through the narrow ShareLookupStore seam and
		// never leaks the workspace id.
		if s.deps.Reports != nil && s.deps.ShareLookup != nil && s.deps.Provenance != nil && s.deps.RunSteps != nil {
			RegisterPublicShareRoutes(r, s.deps.Reports, s.deps.ShareLookup, s.deps.Provenance, s.deps.RunSteps, s.logger)
		}

		// Billing milestone, pass 2: Stripe's UNAUTHENTICATED webhook delivery. Stripe has no
		// Auth.js session cookie — the endpoint self-authenticates by verifying the Stripe-Signature
		// header with STRIPE_WEBHOOK_SECRET (RegisterStripeWebhookRoute's handler rejects
		// unsigned/invalid signatures with 400). Registered on the public /v1 group for that reason.
		if s.deps.BillingUsers != nil && s.deps.BillingEvents != nil && s.deps.BillingClient != nil {
			RegisterStripeWebhookRoute(r, s.deps.BillingUsers, s.deps.BillingEvents, s.deps.BillingClient,
				s.cfg.StripeWebhookSecret, s.cfg.StripePricePro, s.cfg.StripePriceTeam, s.logger)
		}

		// Everything below requires an authenticated session — see internal/authsession's package
		// doc for why this middleware, not web/src/proxy.ts's redirect, is the actual authorization
		// boundary. Guarded on deps.Workspaces being wired so a test server built with a zero-value
		// Dependencies (e.g. this package's own routing/CORS tests) doesn't panic on a nil store.
		if s.deps.Workspaces != nil {
			r.Group(func(r chi.Router) {
				r.Use(authsession.RequireAuth(s.deps.AuthSecret))

				// FEAT-002: create and list workspaces.
				RegisterWorkspaceRoutes(r, s.deps.Workspaces, s.deps.Users, s.logger)

				// FEAT-003: file upload.
				if s.deps.Datasets != nil && s.deps.Objects != nil {
					RegisterUploadRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.Datasets, s.deps.Objects, s.cfg.MaxUploadBytes, s.logger)
				}

				// FEAT-049: pre-upload duplicate-filename check. screens/05-file-upload.md.
				if s.deps.DuplicateCheck != nil {
					RegisterDuplicateCheckRoute(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.DuplicateCheck, s.logger)
				}

				// FEAT-048: single-dataset deletion. screens/04-workspace-home.md's Datasets panel.
				if s.deps.DatasetsList != nil && s.deps.DatasetColumns != nil &&
					s.deps.DatasetRelationships != nil && s.deps.DatasetObjects != nil && s.deps.DatasetDeletionAudit != nil {
					RegisterDatasetRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.DatasetsList, s.deps.DatasetColumns,
						s.deps.DatasetRelationships, s.deps.DatasetObjects, s.deps.DatasetDeletionAudit, s.deps.DatasetStorageReconciler, s.logger)
				}

				// FEAT-042: visible model-egress / audit log.
				if s.deps.AuditLog != nil {
					RegisterAuditRoutes(r, s.deps.Workspaces, s.deps.AuditLog, s.logger)
				}

				// FEAT-004: session-scoped engine provisioning and ingest.
				if s.deps.IngestDatasets != nil && s.deps.IngestPipeline != nil && s.deps.IngestTracker != nil {
					RegisterIngestRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.IngestDatasets, s.deps.IngestPipeline, s.deps.IngestTracker, s.logger)
				}

				// FEAT-005 / FEAT-008: profiling statistics and inferred candidate joins.
				if s.deps.ProfilingDatasets != nil && s.deps.Columns != nil && s.deps.CandidateJoins != nil {
					RegisterProfilingRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.ProfilingDatasets, s.deps.Columns, s.deps.CandidateJoins, s.logger)
				}

				// FEAT-006 / FEAT-007 / FEAT-009: ingest confirmation — type review, PII flag review,
				// and candidate join accept/reject. screens/07-ingest-confirmation.md.
				if s.deps.ConfirmationDatasets != nil && s.deps.ConfirmationColumns != nil &&
					s.deps.ConfirmationCandidateJoins != nil && s.deps.Relationships != nil &&
					s.deps.ConfirmationAudit != nil {
					RegisterConfirmationRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.ConfirmationDatasets,
						s.deps.ConfirmationColumns, s.deps.ConfirmationCandidateJoins, s.deps.Relationships,
						s.deps.ConfirmationAudit, s.logger)
				}

				// FEAT-011: engine sleep/wake lifecycle and honest status.
				if s.deps.EngineProvider != nil && s.deps.EngineDatasets != nil {
					RegisterEngineRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.EngineProvider, s.deps.EngineDatasets, s.logger)
				}

				// FEAT-019: semantic layer storage — relationships read/edit, metric definition CRUD,
				// corrections read, and the workspace-home summary panel.
				// screens/15-semantic-layer.md, screens/04-workspace-home.md.
				if s.deps.SemanticRelationships != nil && s.deps.SemanticMetrics != nil &&
					s.deps.SemanticCorrections != nil && s.deps.SemanticSummary != nil {
					RegisterSemanticRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.SemanticRelationships,
						s.deps.SemanticMetrics, s.deps.SemanticCorrections, s.deps.SemanticSummary, s.logger)
				}

				// FEAT-050/FEAT-051: question suggestions — deterministic strip (no engine, no model),
				// the persisted smart-suggestion bucket (list + delete), and the Pro/Team-gated smart
				// generation that appends to it (engine profile + one model call).
				if s.deps.Suggestions != nil && s.deps.SuggestionBucket != nil {
					RegisterSuggestionRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.Suggestions, s.deps.SuggestionBucket,
						s.deps.SuggestionEngine, s.deps.SuggestionModel, s.deps.SuggestionObjects, s.logger)
				}

				// FEAT-013: ask-a-question run lifecycle — create a run and watch it execute.
				// screens/04-workspace-home.md, screens/08-run-viewer.md.
				// users backs FEAT-021/FEAT-025's Pro/Team tier gate on follow-up/fork run creation.
				if s.deps.Runs != nil && s.deps.RunSteps != nil && s.deps.RunExecutor != nil {
					RegisterRunRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Runs, s.deps.RunSteps, s.deps.Reports, s.deps.Users, s.deps.SemanticCorrections, s.deps.RunExecutor, s.deps.RunEvents, s.logger)
				}

				// FEAT-014/FEAT-030: read a completed run's materialized Report (and, with the
				// provenance seam wired, expand its claim-level provenance links and serve the
				// provenance-panel detail endpoint) — screens/09-report-view.md, screens/10.
				if s.deps.Reports != nil {
					RegisterReportRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Reports, s.deps.Provenance, s.deps.RunSteps, s.logger)
				}

				// FEAT-035..040: report export (PDF, Excel/CSV, PowerPoint, Markdown/HTML, JSON,
				// image) — screens/11-export-dialog.md. Every collaborator is required: the tier gate
				// must never be skippable, so the routes only register when all of them are wired.
				// Provenance supplies the expanded claim objects JSON export embeds (FEAT-039).
				if s.deps.Reports != nil && s.deps.RunSteps != nil && s.deps.Users != nil && s.deps.ExportAudit != nil {
					RegisterExportRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Reports, s.deps.RunSteps, s.deps.Users, s.deps.ExportAudit, s.deps.Provenance, s.deps.ExportNotifications, s.logger)
				}

				// FEAT-045: read-only workspace sharing — invite/list/revoke.
				// screens/13-workspace-settings.md. MemberUsers resolves invite targets by email;
				// Users supplies the authoritative tier lookup for the Team gate.
				if s.deps.Members != nil && s.deps.MemberUsers != nil && s.deps.Users != nil {
					RegisterMemberRoutes(r, s.deps.Workspaces, s.deps.Members, s.deps.MemberUsers, s.deps.Users, s.logger)
				}

				// FEAT-046: read-only report sharing by ID — owner-side create/list/revoke.
				// screens/13-workspace-settings.md.
				if s.deps.Reports != nil && s.deps.Shares != nil && s.deps.Users != nil {
					RegisterReportShareRoutes(r, s.deps.Workspaces, s.deps.Reports, s.deps.Shares, s.deps.Users, s.logger)
				}

				// FEAT-047: user-facing usage view. screens/13-workspace-settings.md. Unlike the
				// other settings tabs, usage is visible to read-only members too (screens/13), so the
				// handler authorizes with requireWorkspaceAccess (owner OR member).
				if s.deps.MemberLookup != nil && s.deps.Usage != nil {
					RegisterUsageRoutes(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Usage, s.logger)
				}

				// FEAT-012: wake-on-intent — POST /v1/workspaces/:id/engine/wake. screens/04.
				if s.deps.EngineWake != nil {
					RegisterEngineWakeRoute(r, s.deps.Workspaces, s.deps.MemberLookup, s.deps.Users, s.deps.EngineWake, s.logger)
				}

				// FEAT-026: durable notifications. screens/16-notifications.md. users enforces the
				// monetization.md row 109 Pro/Team gate.
				if s.deps.Notifications != nil && s.deps.Users != nil {
					RegisterNotificationRoutes(r, s.deps.Notifications, s.deps.Users, s.logger)
				}

				// FEAT-043: one-workspace-one-delete cascade + deletion proof read —
				// screens/14-deletion-confirmation-proof.md.
				if s.deps.WorkspaceCascadeObjects != nil && s.deps.WorkspaceDeletionCascade != nil &&
					s.deps.WorkspaceDeletionProofs != nil && s.deps.WorkspaceDeletionAudit != nil {
					RegisterWorkspaceDeleteRoute(r, s.deps.Workspaces, s.deps.WorkspaceCascadeObjects,
						s.deps.WorkspaceEngineDropper, s.deps.WorkspaceDeletionCascade,
						s.deps.WorkspaceDeletionProofs, s.deps.WorkspaceDeletionAudit, s.logger)
				}

				// Billing milestone, pass 2: authenticated billing endpoints — Checkout Session
				// creation and the subscription/plan read. The webhook lives on the public group
				// above; these need the session. AllowedOrigin builds the Stripe success/cancel URLs.
				if s.deps.BillingUsers != nil && s.deps.BillingClient != nil {
					RegisterBillingRoutes(r, s.deps.BillingUsers, s.deps.BillingClient,
						s.cfg.StripePricePro, s.cfg.StripePriceTeam, s.cfg.AllowedOrigin, s.logger)
				}
			})
		}
	})
}

// handleHealth reports service liveness.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status":      "ok",
		"environment": s.cfg.Environment,
	})
}

// handleNotImplemented returns a handler that honestly reports an unbuilt endpoint.
//
// Foundation registers the route tree so routing, middleware, and CORS are verifiable end to end
// before feature work lands. A 501 naming the owning feature is far more useful than a 404, which
// is indistinguishable from a typo in the path.
func (s *Server) handleNotImplemented(featureID, phase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusNotImplemented, map[string]string{
			"error":   "This endpoint is not implemented yet.",
			"code":    "not_implemented",
			"feature": featureID,
			"phase":   phase,
		})
	}
}

// requestLogger logs one structured line per request, including the correlation id.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(wrapped, r)

		s.logger.Info("request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", wrapped.Status()),
			slog.Duration("duration", time.Since(started)),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)
	})
}

// cors permits credentialed requests from the configured web origin.
//
// The origin is echoed from configuration rather than reflected from the request, and is never "*":
// the browser sends session cookies on every API call, and a wildcard origin with credentials is
// both rejected by browsers and unsafe in principle.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.cfg.AllowedOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// respondJSON writes a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if payload == nil {
		return
	}

	// The header and status are already committed, so an encoding failure cannot be turned into a
	// 500 here. Log-and-continue is the only honest option; the client sees a truncated body.
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("encoding response", slog.String("error", err.Error()))
	}
}
