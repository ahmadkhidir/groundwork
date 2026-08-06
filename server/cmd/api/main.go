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
	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/ingest"
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

	// The status-change hook persists sleeping/waking/ready to workspaces.engine_status as the
	// lifecycle actually transitions (FEAT-011). Screens listing many workspaces can then read the
	// stored value instead of asking the Provider per row — which matters because engine.Provider's
	// contract forbids Status from provoking a wake: merely looking at a workspace must never bill
	// engine-seconds.
	engineProvider := engine.NewLocalProvider(cfg.EngineIdleTimeout, logger,
		engine.WithStatusChangeHook(workspaceRepo.EngineStatusHook(logger)))

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
	ingestPipeline := ingest.NewPipeline(
		datasetRepo, objectStore, engineProvider, columnRepo, candidateJoinRepo, ingestTracker, logger)

	// FEAT-013 (Phase 2). The run executor is the only consumer of the Anthropic key; the model
	// client is constructible without it (Complete fails honestly per-call), so a keyless dev
	// environment still boots — but the gap is logged, not hidden.
	runRepo := postgres.NewRunRepository(pool)
	stepRepo := postgres.NewStepRepository(pool)
	modelClient := model.NewClient(cfg.AnthropicAPIKey, auditWriter, logger)
	if cfg.AnthropicAPIKey == "" {
		logger.Warn("ANTHROPIC_API_KEY is not set — FEAT-013 runs will fail at the model step")
	}
	runExecutor := runpkg.NewExecutor(
		runRepo, stepRepo, datasetRepo, columnRepo, objectStore, engineProvider, auditWriter,
		modelClient, relationshipRepo, metricDefinitionRepo, correctionRepo, logger)

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
		DuplicateCheck:       datasetRepo,

		// FEAT-013: question → run lifecycle. Same repositories as the seams above, satisfying the
		// further narrow interfaces runs.go declares (see Dependencies' doc comment).
		Runs:        runRepo,
		RunSteps:    stepRepo,
		RunExecutor: runExecutor,
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

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		logger.Info("shutdown started")

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
