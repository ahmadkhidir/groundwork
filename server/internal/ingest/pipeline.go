// Package ingest turns an uploaded file into normalized, durably-stored Parquet, per FEAT-004
// (Session-Scoped Engine Provisioning & Ingest) and architecture.md's Ingest & Profiling Pipeline
// component.
//
// Scope, deliberately: this package owns only "Provisioning engine" and "Loading data" — the first
// two of screens/06-ingest-progress.md's four steps. It hands off to "profiling" (FEAT-005) and
// "relationship inference" (FEAT-008) by leaving the dataset at ingest_status "profiling" once the
// normalized Parquet is durable; it never computes column statistics or infers joins itself.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/storage"
)

// DatasetStore is the persistence seam Pipeline depends on, satisfied by
// internal/store/postgres.DatasetRepository.
type DatasetStore interface {
	GetByID(ctx context.Context, id string) (domain.Dataset, error)
	SetIngestStatus(ctx context.Context, id string, status domain.IngestStatus) error
	CompleteLoad(ctx context.Context, id, parquetLocation string, rowCount int64) error
	MarkFailed(ctx context.Context, id, reason string) error
}

// ObjectStore is the persistence seam for the uploaded original and the normalized Parquet output,
// satisfied by internal/storage.Client.
type ObjectStore interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	PutObject(ctx context.Context, key string, content io.Reader, size int64, contentType string) error
}

// EngineProvider is the Engine Provider seam this package consumes, satisfied by
// internal/engine.Provider. Defined consumer-side, narrowed to what the pipeline actually needs.
type EngineProvider interface {
	Acquire(ctx context.Context, workspaceID string) (engine.Handle, error)
	Release(handle engine.Handle) error
}

// Pipeline turns one pending Dataset into normalized, durable Parquet.
//
// A Pipeline is stateless between runs beyond its dependencies and the Tracker — every call to Start
// is independent, matching how datasets arrive (one row per uploaded file, ingested independently).
type Pipeline struct {
	datasets DatasetStore
	objects  ObjectStore
	engines  EngineProvider
	tracker  *Tracker
	logger   *slog.Logger

	// tempDir is where the original file and the intermediate Parquet output are staged on local
	// disk while the engine (which needs filesystem paths, not byte streams) works on them. Defaults
	// to os.TempDir(); overridable in tests.
	tempDir string
}

// NewPipeline constructs a Pipeline.
func NewPipeline(datasets DatasetStore, objects ObjectStore, engines EngineProvider, tracker *Tracker, logger *slog.Logger) *Pipeline {
	return &Pipeline{
		datasets: datasets,
		objects:  objects,
		engines:  engines,
		tracker:  tracker,
		logger:   logger,
		tempDir:  os.TempDir(),
	}
}

// Start begins ingesting datasetID in the background and returns immediately.
//
// Deliberately decoupled from any request context: screens/06's edge case requires that closing the
// browser tab mid-ingest does not lose progress, because "ingest continues server-side." A goroutine
// rooted in context.Background() (not the HTTP handler's request context, which is cancelled the
// moment the client disconnects) is what makes that true.
//
// Idempotent: if the dataset is not currently "pending", this is a no-op — a duplicate call (e.g. the
// Ingest Progress screen re-mounting) never restarts an in-flight or already-finished ingest.
func (p *Pipeline) Start(datasetID string) {
	ctx := context.Background()

	ds, err := p.datasets.GetByID(ctx, datasetID)
	if err != nil {
		p.logger.Error("ingest: loading dataset to start", slog.String("dataset_id", datasetID), slog.String("error", err.Error()))
		return
	}
	if ds.IngestStatus != domain.IngestPending {
		return
	}

	p.tracker.Set(ds.ID, StepProvisioning, "")
	go p.run(ctx, ds)
}

// run executes the full provisioning + loading pipeline for one dataset. Every exit path either
// completes the handoff to "profiling" or marks the dataset "failed" with a specific reason — no
// path leaves a dataset stuck at "pending" or "provisioning" indefinitely.
func (p *Pipeline) run(ctx context.Context, ds domain.Dataset) {
	if err := p.datasets.SetIngestStatus(ctx, ds.ID, domain.IngestProvisioning); err != nil {
		p.logger.Error("ingest: recording provisioning status", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()))
		// Not fatal to the run itself — the tracker still reflects live progress even if this one
		// persisted checkpoint write failed; a retry of the write isn't attempted mid-pipeline since
		// the terminal write (CompleteLoad/MarkFailed) is what must land for correctness.
	}
	p.tracker.Set(ds.ID, StepProvisioning, "")

	handle, err := p.engines.Acquire(ctx, ds.WorkspaceID)
	if err != nil {
		p.fail(ctx, ds, "Could not start the analytical engine for this workspace. Try again in a moment.")
		return
	}
	defer func() {
		if releaseErr := p.engines.Release(handle); releaseErr != nil {
			p.logger.Error("ingest: releasing engine handle", slog.String("dataset_id", ds.ID), slog.String("error", releaseErr.Error()))
		}
	}()

	p.tracker.Set(ds.ID, StepLoading, "")

	parquetLocation, rows, err := p.loadAndNormalize(ctx, handle, ds)
	if err != nil {
		p.fail(ctx, ds, err.Error())
		return
	}

	if err := p.datasets.CompleteLoad(ctx, ds.ID, parquetLocation, rows); err != nil {
		p.fail(ctx, ds, "Ingest finished but could not be recorded. Try again.")
		return
	}

	// Hand off: this dataset now sits at ingest_status "profiling", the checkpoint FEAT-005 picks up
	// from. This package's job is done — it does not compute statistics or advance the status
	// further. The tracker entry is cleared rather than left at StepProfiling: with no later wave
	// wired up yet to advance it, the live SSE stream should fall back to reading the persisted
	// "profiling" status directly (an honest "next step, not yet started") rather than this package
	// asserting a live-progress state it has no further authority over.
	p.tracker.Clear(ds.ID)
	p.logger.Info("ingest: load complete, handed off to profiling",
		slog.String("dataset_id", ds.ID),
		slog.String("workspace_id", ds.WorkspaceID),
		slog.Int64("row_count", rows),
	)
}

// loadAndNormalize downloads the original upload, runs it through the engine to produce normalized
// Parquet, uploads that Parquet to its canonical location, and returns that location plus the row
// count. Every returned error is already a specific, actionable message.
func (p *Pipeline) loadAndNormalize(ctx context.Context, handle engine.Handle, ds domain.Dataset) (string, int64, error) {
	sourceKey := storage.DatasetKey(ds.WorkspaceID, ds.ID, ds.OriginalFilename)

	sourcePath, cleanup, err := p.downloadToTemp(ctx, sourceKey, filepath.Ext(ds.OriginalFilename))
	if err != nil {
		return "", 0, errors.New("could not read the uploaded file. Try uploading it again.")
	}
	defer cleanup()

	parquetPath := filepath.Join(p.tempDir, "groundwork-ingest-"+ds.ID+".parquet")
	defer os.Remove(parquetPath)

	if err := normalizeToParquet(ctx, handle, ds.Format, sourcePath, parquetPath); err != nil {
		return "", 0, err
	}

	rows, err := rowCount(ctx, handle, parquetPath)
	if err != nil {
		return "", 0, err
	}

	parquetFile, err := os.Open(parquetPath)
	if err != nil {
		return "", 0, errors.New("could not read the normalized output. Try again.")
	}
	defer parquetFile.Close()

	info, err := parquetFile.Stat()
	if err != nil {
		return "", 0, errors.New("could not read the normalized output. Try again.")
	}

	destKey := storage.NormalizedDatasetKey(ds.WorkspaceID, ds.ID)
	if err := p.objects.PutObject(ctx, destKey, parquetFile, info.Size(), "application/octet-stream"); err != nil {
		p.logger.Error("ingest: uploading normalized parquet", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()), slog.String("size", formatByteCount(info.Size())))
		return "", 0, errors.New("could not store the normalized data. Try again.")
	}

	return destKey, rows, nil
}

// downloadToTemp copies the object at key to a local temp file (the engine needs a filesystem path,
// not a byte stream) and returns its path plus a cleanup func the caller must defer.
func (p *Pipeline) downloadToTemp(ctx context.Context, key, ext string) (string, func(), error) {
	reader, err := p.objects.GetObject(ctx, key)
	if err != nil {
		return "", func() {}, fmt.Errorf("fetching uploaded object %q: %w", key, err)
	}
	defer reader.Close()

	f, err := os.CreateTemp(p.tempDir, "groundwork-ingest-*"+ext)
	if err != nil {
		return "", func() {}, fmt.Errorf("creating temp file: %w", err)
	}
	cleanup := func() { os.Remove(f.Name()) }

	if _, err := io.Copy(f, reader); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("staging uploaded object %q: %w", key, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("closing staged file: %w", err)
	}

	return f.Name(), cleanup, nil
}

// fail records a specific, actionable failure and clears any live tracker state — a dataset never
// stays "in progress" in the Tracker once it has a persisted terminal state.
func (p *Pipeline) fail(ctx context.Context, ds domain.Dataset, reason string) {
	p.tracker.Set(ds.ID, StepFailed, reason)
	if err := p.datasets.MarkFailed(ctx, ds.ID, reason); err != nil {
		p.logger.Error("ingest: recording failure", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()))
	}
	p.logger.Warn("ingest: failed",
		slog.String("dataset_id", ds.ID),
		slog.String("workspace_id", ds.WorkspaceID),
		slog.String("reason", reason),
	)
}

// StateFor resolves the best-known live state for datasetID: the Tracker's in-memory entry if this
// process has one, otherwise a state derived from ds's persisted ingest_status — the fallback used
// after a server restart or for a dataset another process instance is handling.
func StateFor(tracker *Tracker, ds domain.Dataset) State {
	if s, ok := tracker.Get(ds.ID); ok {
		return s
	}

	switch ds.IngestStatus {
	case domain.IngestPending, domain.IngestProvisioning:
		return State{Step: StepProvisioning, UpdatedAt: time.Now()}
	case domain.IngestProfiling:
		return State{Step: StepProfiling, UpdatedAt: time.Now()}
	case domain.IngestFailed:
		return State{Step: StepFailed, UpdatedAt: time.Now()}
	default:
		return State{Step: StepProfiling, UpdatedAt: time.Now()}
	}
}
