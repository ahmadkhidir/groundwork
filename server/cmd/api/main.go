// Command api is the Groundwork API service — the single backend.
//
// It owns the database, the engine seam, the orchestrator, and every authorization decision. The
// Next.js tier renders and proxies; it never does any of that work itself.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/api"
	"github.com/ahmadkhidir/groundwork/server/internal/audit"
	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/billing"
	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
	"github.com/ahmadkhidir/groundwork/server/internal/ingest"
	"github.com/ahmadkhidir/groundwork/server/internal/metering"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	runpkg "github.com/ahmadkhidir/groundwork/server/internal/run"
	"github.com/ahmadkhidir/groundwork/server/internal/storage"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	if err := run(logger); err != nil {
		logger.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run starts the server and blocks until shutdown completes.
//
// Separated from main so every exit path returns an error instead of calling os.Exit, which would
// skip deferred cleanup.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// A startup timeout, distinct from the request-scoped contexts used once the server is serving
	// traffic: connecting to Postgres must not be allowed to hang forever if the database is
	// unreachable.
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStartup()

	pool, err := postgres.NewPool(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	userRepo := postgres.NewUserRepository(pool)
	auditRepo := postgres.NewAuditRepository(pool)
	auditWriter := audit.NewWriter(auditRepo)

	authService := auth.NewService(userRepo, logger)
	authService.SetLoginAuditor(auditWriter)

	objectStore := storage.NewClient(storage.Config{
		Endpoint:        cfg.Storage.Endpoint,
		Bucket:          cfg.Storage.Bucket,
		AccessKeyID:     cfg.Storage.AccessKeyID,
		SecretAccessKey: cfg.Storage.SecretAccessKey,
		Region:          cfg.Storage.Region,
	})

	// The Engine Provider seam (internal/engine.Provider), wired here rather than in internal/api so
	// no HTTP-layer package ever needs to know it's DuckDB, or engine.LocalProvider specifically —
	// architecture.md's "implementation #1" of the seam, running in-process rather than as a Fly
	// Machine (see engine/local_provider.go's doc comment).
	workspaceRepo := postgres.NewWorkspaceRepository(pool)

	// Billing milestone, pass 1 (usage metering). usageWriter is the single write path into
	// usage_records (accumulate for engine-seconds/tokens, replace for storage_bytes); the storage
	// reconciler recomputes a workspace's storage snapshot from object storage. Both are consumed by
	// the engine hook, the ingest pipeline, the model client, and the dataset-delete handler below.
	usageRepo := postgres.NewUsageRepository(pool)
	usageWriter := metering.NewUsageWriter(usageRepo)
	storageReconciler := metering.NewStorageReconciler(objectStore, usageWriter)

	// The status-change hook persists sleeping/waking/ready to workspaces.engine_status as the
	// lifecycle actually transitions (FEAT-011). Screens listing many workspaces can then read the
	// stored value instead of asking the Provider per row — which matters because engine.Provider's
	// contract forbids Status from provoking a wake: merely looking at a workspace must never bill
	// engine-seconds.
	//
	// The usage hook meters engine-seconds on every Release (best-effort — see engine.UsageHookFunc).
	engineProvider := engine.NewLocalProvider(cfg.EngineIdleTimeout, logger,
		engine.WithStatusChangeHook(workspaceRepo.EngineStatusHook(logger)),
		engine.WithUsageHook(func(workspaceID string, seconds float64) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return usageWriter.Record(ctx, workspaceID, seconds, 0)
		}))

	datasetRepo := postgres.NewDatasetRepository(pool)
	columnRepo := postgres.NewColumnRepository(pool)
	candidateJoinRepo := postgres.NewCandidateJoinRepository(pool)
	relationshipRepo := postgres.NewRelationshipRepository(pool)

	// FEAT-019 (Semantic Layer Storage). relationshipRepo above already satisfies
	// api.SemanticRelationshipStore — same underlying table, wider interface — so it is reused
	// rather than constructed twice.
	metricDefinitionRepo := postgres.NewMetricDefinitionRepository(pool)
	correctionRepo := postgres.NewCorrectionRepository(pool)
	semanticSummaryRepo := postgres.NewSemanticSummaryRepository(pool)

	ingestTracker := ingest.NewTracker()
	// FEAT-026's notification repository is constructed here (before the pipeline) because the
	// pipeline's ingest-failure event source consumes it via WithNotifications.
	notificationRepo := postgres.NewNotificationRepository(pool)
	ingestPipeline := ingest.NewPipeline(
		datasetRepo, objectStore, engineProvider, columnRepo, candidateJoinRepo, ingestTracker, logger,
		// FEAT-026: write an `ingest_failed` durable notification when an upload fails (best-effort).
		ingest.WithNotifications(notificationRepo, workspaceRepo),
		// Billing milestone, pass 1: refresh the workspace's storage_bytes snapshot after a
		// successful ingest (best-effort).
		ingest.WithStorageReconciler(storageReconciler))

	// FEAT-013 (Phase 2). The run executor is the only consumer of the Anthropic key; the model
	// client is constructible without it (Complete fails honestly per-call), so a keyless dev
	// environment still boots — but the gap is logged, not hidden.
	runRepo := postgres.NewRunRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)
	// FEAT-014's Report Store is materialized by the executor and read by the report read endpoint.
	reportRepo := postgres.NewReportRepository(pool)
	// FEAT-030's claim-level provenance store: written by the executor at report materialization
	// (WithProvenance) and read by the report/provenance endpoints. Same postgres repository both ways.
	provenanceRepo := postgres.NewProvenanceRepository(pool)
	// FEAT-043's deletion-proof read side: the deletion_records table deliberately survives the
	// workspace it describes, so this repository is the only store that can be read AFTER a
	// workspace is gone (screens/14's follow-up proof GET).
	deletionRecordRepo := postgres.NewDeletionRecordRepository(pool)

	// FEAT-045 (workspace_members), FEAT-046 (report_shares), and FEAT-026 (notifications) — Phase 5.
	// usageRepo was constructed above for the billing metering writer; FEAT-047's read endpoint uses
	// the same repository.
	memberRepo := postgres.NewMemberRepository(pool)
	shareRepo := postgres.NewReportShareRepository(pool)

	// Billing milestone, pass 2 (Stripe integration). billingClient wraps the Stripe SDK; the event
	// repository dedupes webhook deliveries by event id and records the durable billing-event trail.
	billingClient := billing.NewClient(cfg.StripeSecretKey)
	billingEventRepo := postgres.NewBillingEventRepository(pool)
	if cfg.StripeSecretKey == "" || cfg.StripeWebhookSecret == "" {
		logger.Warn("Stripe is not configured — checkout and webhook endpoints will fail honestly")
	}

	modelClient := model.NewClient(cfg.AnthropicAPIKey, auditWriter, logger).WithUsageRecorder(usageWriter)
	if cfg.AnthropicAPIKey == "" {
		logger.Warn("ANTHROPIC_API_KEY is not set — FEAT-013 runs will fail at the model step")
	}
	// FEAT-023's live run-event fan-out and FEAT-027's determinism cache are wired together here: the
	// executor publishes to the hub (WithEvents) and the same hub serves the run stream endpoint;
	// the cache repository is what lets an identical later run replay persisted outputs instead of
	// re-asking the model (WithCache).
	runEvents := events.NewHub()
	runCacheRepo := postgres.NewRunCacheRepository(pool)
	runExecutor := runpkg.NewExecutor(
		runRepo, stepRepo, datasetRepo, columnRepo, objectStore, engineProvider, auditWriter,
		modelClient, reportRepo, relationshipRepo, metricDefinitionRepo, correctionRepo, logger,
		runpkg.WithEvents(runEvents), runpkg.WithCache(runCacheRepo), runpkg.WithProvenance(provenanceRepo))

	deps := api.Dependencies{
		AuthSecret: cfg.AuthSecret,
		Workspaces: workspaceRepo,
		Users:      userRepo,
		Datasets:   datasetRepo,
		Objects:    objectStore,
		AuditLog:   auditRepo,

		IngestDatasets: datasetRepo,
		IngestPipeline: ingestPipeline,
		IngestTracker:  ingestTracker,

		// One DatasetRepository satisfies three deliberately narrow seams; see Dependencies' doc.
		ProfilingDatasets: datasetRepo,
		Columns:           columnRepo,
		CandidateJoins:    candidateJoinRepo,

		// screens/07-ingest-confirmation.md's write endpoints (FEAT-006/FEAT-007/FEAT-009). Same
		// underlying repositories as above, satisfying a further, deliberately narrow set of
		// interfaces — see Dependencies' doc comment.
		ConfirmationDatasets:       datasetRepo,
		ConfirmationColumns:        columnRepo,
		ConfirmationCandidateJoins: candidateJoinRepo,
		Relationships:              relationshipRepo,
		ConfirmationAudit:          auditWriter,

		EngineProvider: engineProvider,
		EngineDatasets: datasetRepo,

		// FEAT-019: semantic layer storage. screens/15-semantic-layer.md, screens/04-workspace-home.md.
		SemanticRelationships: relationshipRepo,
		SemanticMetrics:       metricDefinitionRepo,
		SemanticCorrections:   correctionRepo,
		SemanticSummary:       semanticSummaryRepo,

		// FEAT-048 (single-dataset deletion) and FEAT-049 (duplicate-filename detection). Same
		// datasetRepo/relationshipRepo/objectStore/auditWriter instances as above, satisfying the
		// further narrow seams datasets.go and upload.go declare — see api.Dependencies' doc comment.
		DatasetsList:         datasetRepo,
		DatasetColumns:       columnRepo,
		DatasetRelationships: relationshipRepo,
		DatasetObjects:       objectStore,
		DatasetDeletionAudit: auditWriter,
		// Billing milestone, pass 1: refresh the workspace's storage snapshot after a dataset delete.
		DatasetStorageReconciler: storageReconciler,
		DuplicateCheck:           datasetRepo,

		// FEAT-013: question → run lifecycle. Same repositories as the seams above, satisfying the
		// further narrow interfaces runs.go declares (see Dependencies' doc comment).
		Runs:        runRepo,
		RunSteps:    stepRepo,
		RunExecutor: runExecutor,
		RunEvents:   runEvents,

		// FEAT-014/FEAT-030: report read endpoint (screens/09-report-view.md) + claim-level
		// provenance (screens/10-provenance-panel.md). Materialized and written by the executor;
		// read here through the same repository instances.
		Reports:    reportRepo,
		Provenance: provenanceRepo,

		// FEAT-035/FEAT-036: report export audit entries, written in the same handler that serves
		// the export bytes (screens/11-export-dialog.md). Same auditWriter as ConfirmationAudit.
		ExportAudit: auditWriter,

		// FEAT-043 (One-Workspace-One-Delete Cascade): objectStore deletes every object under the
		// workspace's key prefix; engineProvider drops the workspace's session-scoped engine state
		// (engine.Drop, added with this feature); workspaceRepo runs the database half of the
		// cascade; deletionRecordRepo reads the surviving proof; auditWriter writes the cascade's
		// own audit entry before the workspace's log rows are destroyed (screens/14).
		WorkspaceCascadeObjects:  objectStore,
		WorkspaceEngineDropper:   engineProvider,
		WorkspaceDeletionCascade: workspaceRepo,
		WorkspaceDeletionProofs:  deletionRecordRepo,
		WorkspaceDeletionAudit:   auditWriter,

		// FEAT-045 (workspace sharing): same memberRepo satisfies the management seam and the
		// narrow access-check lookup; userRepo answers invite-by-email. FEAT-046 (report shares):
		// shareRepo. FEAT-047 (usage): usageRepo. FEAT-012 (wake-on-intent): engineProvider.
		// FEAT-026 (durable notifications): notificationRepo (read side; the event sources that
		// create notifications are wired in assembly).
		Members:             memberRepo,
		MemberLookup:        memberRepo,
		MemberUsers:         userRepo,
		Shares:              shareRepo,
		ShareLookup:         shareRepo,
		Usage:               usageRepo,
		EngineWake:          engineProvider,
		Notifications:       notificationRepo,
		ExportNotifications: notificationRepo,

		// Billing milestone, pass 2: userRepo is the only seam allowed to change users.tier;
		// billingEventRepo dedupes/records webhook events; billingClient wraps the Stripe SDK.
		BillingUsers:  userRepo,
		BillingEvents: billingEventRepo,
		BillingClient: billingClient,
	}

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: api.NewServer(cfg, logger, authService, deps),
		// Timeouts are set explicitly: Go's defaults are none, which leaves the service open to
		// slow-client resource exhaustion.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Generous, because ingest uploads and long analytical queries legitimately take a while.
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	// Buffered so a signal arriving before the receive is not dropped.
	shutdownErr := make(chan error, 1)

	// Billing milestone, pass 1: daily storage-bytes reconciliation — a background loop, not an
	// endpoint. Metering is infrastructure and must not depend on a client noticing to call it.
	// Runs once at startup, then on cfg.UsageReconcileInterval; cancelled on shutdown.
	meteringCtx, cancelMetering := context.WithCancel(context.Background())
	defer cancelMetering()
	go runStorageReconciliation(meteringCtx, cfg, storageReconciler, workspaceRepo, logger)

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		logger.Info("shutdown started")
		cancelMetering()

		// Graceful shutdown matters more here than in a typical CRUD service: an in-flight run is
		// resumable only if its current step is persisted, so in-flight requests get time to reach
		// their next boundary rather than being cut off mid-step.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		shutdownErr <- server.Shutdown(ctx)
	}()

	logger.Info("server listening",
		slog.String("port", cfg.Port),
		slog.String("environment", cfg.Environment),
		slog.Int64("max_upload_bytes", cfg.MaxUploadBytes),
	)

	// ErrServerClosed is the expected outcome of a graceful shutdown, not a failure.
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	if err := <-shutdownErr; err != nil {
		return err
	}

	logger.Info("shutdown complete")
	return nil
}

// runStorageReconciliation drives metering.ReconcileStorage for every workspace on the configured
// interval until ctx is cancelled — the daily backstop that keeps usage_records' storage_bytes
// snapshots truthful even when an event-driven snapshot was missed.
//
// Best-effort per workspace: a failing workspace (transient storage/DB error) is logged and
// skipped; the sweep continues. Runs once immediately at startup so a fresh deployment does not
// wait a full interval for its first snapshot.
func runStorageReconciliation(ctx context.Context, cfg config.Config, reconciler metering.Reconciler, workspaces *postgres.WorkspaceRepository, logger *slog.Logger) {
	reconcileAllWorkspaces(ctx, reconciler, workspaces, logger)
	ticker := time.NewTicker(cfg.UsageReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileAllWorkspaces(ctx, reconciler, workspaces, logger)
		}
	}
}

// reconcileAllWorkspaces runs one reconciliation pass over every workspace, with a per-workspace
// timeout so a hanging object-store listing for one workspace cannot stall the sweep.
func reconcileAllWorkspaces(ctx context.Context, reconciler metering.Reconciler, workspaces *postgres.WorkspaceRepository, logger *slog.Logger) {
	all, err := workspaces.ListAll(ctx)
	if err != nil {
		logger.Error("metering: listing workspaces for storage reconciliation", slog.String("error", err.Error()))
		return
	}
	for _, ws := range all {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := reconciler.Reconcile(wctx, ws.ID)
		cancel()
		if err != nil {
			logger.Error("metering: reconciling storage usage", slog.String("workspace_id", ws.ID), slog.String("error", err.Error()))
		}
	}
}
