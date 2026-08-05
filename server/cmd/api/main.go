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
	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/config"
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

	authService := auth.NewService(postgres.NewUserRepository(pool), logger)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: api.NewServer(cfg, logger, authService),
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
