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

	// Runs, RunSteps, and RunExecutor back FEAT-013 (Natural-Language Question → SQL Generation) —
	// screens/04-workspace-home.md's Ask panel and screens/08-run-viewer.md's live run. Runs is the
	// create/get seam over the runs table; RunSteps exposes a run's persisted step feed to the
	// viewer; RunExecutor starts the background linear executor (internal/run.Executor) — the only
	// thing allowed to create step rows or advance run status after the handler's queued insert.
	Runs        RunStore
	RunSteps    RunStepStore
	RunExecutor RunExecutor

	// Reports backs FEAT-014's report read endpoint — screens/09-report-view.md's single self-
	// contained fetch. Satisfied by postgres.ReportRepository, the same instance the executor uses
	// to materialize reports on run completion.
	Reports ReportStore
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
					RegisterUploadRoutes(r, s.deps.Workspaces, s.deps.Datasets, s.deps.Objects, s.cfg.MaxUploadBytes, s.logger)
				}

				// FEAT-049: pre-upload duplicate-filename check. screens/05-file-upload.md.
				if s.deps.DuplicateCheck != nil {
					RegisterDuplicateCheckRoute(r, s.deps.Workspaces, s.deps.DuplicateCheck, s.logger)
				}

				// FEAT-048: single-dataset deletion. screens/04-workspace-home.md's Datasets panel.
				if s.deps.DatasetsList != nil && s.deps.DatasetColumns != nil &&
					s.deps.DatasetRelationships != nil && s.deps.DatasetObjects != nil && s.deps.DatasetDeletionAudit != nil {
					RegisterDatasetRoutes(r, s.deps.Workspaces, s.deps.DatasetsList, s.deps.DatasetColumns,
						s.deps.DatasetRelationships, s.deps.DatasetObjects, s.deps.DatasetDeletionAudit, s.logger)
				}

				// FEAT-042: visible model-egress / audit log.
				if s.deps.AuditLog != nil {
					RegisterAuditRoutes(r, s.deps.Workspaces, s.deps.AuditLog, s.logger)
				}

				// FEAT-004: session-scoped engine provisioning and ingest.
				if s.deps.IngestDatasets != nil && s.deps.IngestPipeline != nil && s.deps.IngestTracker != nil {
					RegisterIngestRoutes(r, s.deps.Workspaces, s.deps.IngestDatasets, s.deps.IngestPipeline, s.deps.IngestTracker, s.logger)
				}

				// FEAT-005 / FEAT-008: profiling statistics and inferred candidate joins.
				if s.deps.ProfilingDatasets != nil && s.deps.Columns != nil && s.deps.CandidateJoins != nil {
					RegisterProfilingRoutes(r, s.deps.Workspaces, s.deps.ProfilingDatasets, s.deps.Columns, s.deps.CandidateJoins, s.logger)
				}

				// FEAT-006 / FEAT-007 / FEAT-009: ingest confirmation — type review, PII flag review,
				// and candidate join accept/reject. screens/07-ingest-confirmation.md.
				if s.deps.ConfirmationDatasets != nil && s.deps.ConfirmationColumns != nil &&
					s.deps.ConfirmationCandidateJoins != nil && s.deps.Relationships != nil &&
					s.deps.ConfirmationAudit != nil {
					RegisterConfirmationRoutes(r, s.deps.Workspaces, s.deps.ConfirmationDatasets,
						s.deps.ConfirmationColumns, s.deps.ConfirmationCandidateJoins, s.deps.Relationships,
						s.deps.ConfirmationAudit, s.logger)
				}

				// FEAT-011: engine sleep/wake lifecycle and honest status.
				if s.deps.EngineProvider != nil && s.deps.EngineDatasets != nil {
					RegisterEngineRoutes(r, s.deps.Workspaces, s.deps.EngineProvider, s.deps.EngineDatasets, s.logger)
				}

				// FEAT-019: semantic layer storage — relationships read/edit, metric definition CRUD,
				// corrections read, and the workspace-home summary panel.
				// screens/15-semantic-layer.md, screens/04-workspace-home.md.
				if s.deps.SemanticRelationships != nil && s.deps.SemanticMetrics != nil &&
					s.deps.SemanticCorrections != nil && s.deps.SemanticSummary != nil {
					RegisterSemanticRoutes(r, s.deps.Workspaces, s.deps.SemanticRelationships,
						s.deps.SemanticMetrics, s.deps.SemanticCorrections, s.deps.SemanticSummary, s.logger)
				}

				// FEAT-013: ask-a-question run lifecycle — create a run and watch it execute.
				// screens/04-workspace-home.md, screens/08-run-viewer.md.
				if s.deps.Runs != nil && s.deps.RunSteps != nil && s.deps.RunExecutor != nil {
					RegisterRunRoutes(r, s.deps.Workspaces, s.deps.Runs, s.deps.RunSteps, s.deps.Reports, s.deps.SemanticCorrections, s.deps.RunExecutor, s.logger)
				}

				// FEAT-014: read a completed run's materialized Report — screens/09-report-view.md.
				if s.deps.Reports != nil {
					RegisterReportRoutes(r, s.deps.Workspaces, s.deps.Reports, s.logger)
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
