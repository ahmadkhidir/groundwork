// Package ingest turns an uploaded file into normalized, durably-stored Parquet, profiles it, and —
// when 2+ files share a workspace — infers candidate joins between them, per FEAT-004
// (Session-Scoped Engine Provisioning & Ingest), FEAT-005 (Data Profiling), FEAT-008 (Multi-File
// Relationship Inference), and architecture.md's Ingest & Profiling Pipeline component.
//
// This package drives all four of screens/06-ingest-progress.md's steps: "Provisioning engine",
// "Loading data" (FEAT-004), "Profiling columns" (FEAT-005), and — only when the workspace has 2+
// files — "Finding relationships" (FEAT-008). It hands off to ingest_status "complete" once every
// applicable step has finished; the actual computation of column statistics and candidate joins is
// delegated to internal/profiling and internal/relationships respectively, both consumed narrowly
// through the Engine Provider seam this package already holds an acquired Handle for.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/profiling"
	"github.com/ahmadkhidir/groundwork/server/internal/relationships"
	"github.com/ahmadkhidir/groundwork/server/internal/storage"
)

// DatasetStore is the persistence seam Pipeline depends on, satisfied by
// internal/store/postgres.DatasetRepository.
type DatasetStore interface {
	GetByID(ctx context.Context, id string) (domain.Dataset, error)
	// ListByWorkspace returns every dataset in a workspace, used to find already-"complete" sibling
	// datasets when deciding whether relationship inference (FEAT-008) applies.
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
	SetIngestStatus(ctx context.Context, id string, status domain.IngestStatus) error
	CompleteLoad(ctx context.Context, id, parquetLocation string, rowCount int64) error
	// CompleteIngest marks the true end of the pipeline, once profiling (and relationship inference,
	// when applicable) has finished for this dataset.
	CompleteIngest(ctx context.Context, id string) error
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

// ColumnStore is the persistence seam for profiled columns (FEAT-005), satisfied by
// internal/store/postgres.ColumnRepository.
type ColumnStore interface {
	BulkInsert(ctx context.Context, workspaceID string, cols []domain.Column) error
	ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error)
}

// CandidateJoinStore is the persistence seam for inferred candidate joins (FEAT-008), satisfied by
// internal/store/postgres.CandidateJoinRepository.
type CandidateJoinStore interface {
	Create(ctx context.Context, cj domain.CandidateJoin) error
	// ExistsForPair reports whether a candidate already exists between two columns, in either
	// direction — see CandidateJoinRepository.ExistsForPair's doc comment for why this guards both
	// duplicate inserts and reintroducing a rejected join.
	ExistsForPair(ctx context.Context, columnAID, columnBID string) (bool, error)
}

// Pipeline turns one pending Dataset into normalized, durable, profiled Parquet, inferring candidate
// joins against any already-complete sibling datasets in the same workspace along the way.
//
// A Pipeline is stateless between runs beyond its dependencies and the Tracker — every call to Start
// is independent, matching how datasets arrive (one row per uploaded file, ingested independently).
type Pipeline struct {
	datasets DatasetStore
	objects  ObjectStore
	engines  EngineProvider
	columns  ColumnStore
	joins    CandidateJoinStore
	tracker  *Tracker
	logger   *slog.Logger

	// tempDir is where the original file and the intermediate Parquet output are staged on local
	// disk while the engine (which needs filesystem paths, not byte streams) works on them. Defaults
	// to os.TempDir(); overridable in tests.
	tempDir string

	// wsLocksMu guards wsLocks, the map of per-workspace mutexes below.
	wsLocksMu sync.Mutex
	// wsLocks serializes the "find my completed siblings, infer relationships, mark myself complete"
	// sequence per workspace. Two datasets in the same batch typically finish profiling around the
	// same time on independent goroutines (Start spawns one per dataset); without this lock, both
	// could read the sibling list before either has marked itself complete and each would conclude it
	// has no complete siblings, silently skipping FEAT-008 for that pair. Serializing per workspace
	// means whichever dataset finishes second always observes the first as a complete sibling.
	wsLocks map[string]*sync.Mutex
}

// NewPipeline constructs a Pipeline.
func NewPipeline(datasets DatasetStore, objects ObjectStore, engines EngineProvider, columns ColumnStore, joins CandidateJoinStore, tracker *Tracker, logger *slog.Logger) *Pipeline {
	return &Pipeline{
		datasets: datasets,
		objects:  objects,
		engines:  engines,
		columns:  columns,
		joins:    joins,
		tracker:  tracker,
		logger:   logger,
		tempDir:  os.TempDir(),
		wsLocks:  make(map[string]*sync.Mutex),
	}
}

// workspaceLock returns the mutex serializing relationship-inference-and-completion for workspaceID,
// creating it on first use.
func (p *Pipeline) workspaceLock(workspaceID string) *sync.Mutex {
	p.wsLocksMu.Lock()
	defer p.wsLocksMu.Unlock()
	lock, ok := p.wsLocks[workspaceID]
	if !ok {
		lock = &sync.Mutex{}
		p.wsLocks[workspaceID] = lock
	}
	return lock
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

	// Claim, not Set: guards against a duplicate concurrent call for the SAME still-"pending"
	// dataset (e.g. the Ingest Progress screen's mount effect firing twice — React Strict Mode's
	// double-invoke in development reproduces this reliably, and a client reconnect can too)
	// spawning a second run() before the first has persisted its own status change. See
	// Tracker.Claim's doc comment.
	if !p.tracker.Claim(ds.ID) {
		return
	}
	go p.run(ctx, ds)
}

// run executes the full pipeline for one dataset — provisioning, loading, profiling, and (when
// applicable) relationship inference. Every exit path either reaches ingest_status "complete" or
// marks the dataset "failed" with a specific reason — no path leaves a dataset stuck at "pending",
// "provisioning", or "profiling" indefinitely.
func (p *Pipeline) run(ctx context.Context, ds domain.Dataset) {
	if err := p.datasets.SetIngestStatus(ctx, ds.ID, domain.IngestProvisioning); err != nil {
		p.logger.Error("ingest: recording provisioning status", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()))
		// Not fatal to the run itself — the tracker still reflects live progress even if this one
		// persisted checkpoint write failed; a retry of the write isn't attempted mid-pipeline since
		// the terminal write (CompleteIngest/MarkFailed) is what must land for correctness.
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

	parquetLocation, localParquetPath, rows, cleanupParquet, err := p.loadAndNormalize(ctx, handle, ds)
	if err != nil {
		p.fail(ctx, ds, err.Error())
		return
	}
	defer cleanupParquet()

	if err := p.datasets.CompleteLoad(ctx, ds.ID, parquetLocation, rows); err != nil {
		p.fail(ctx, ds, "Ingest finished but could not be recorded. Try again.")
		return
	}
	ds.ParquetLocation = parquetLocation
	ds.RowCount = rows

	p.tracker.Set(ds.ID, StepProfiling, "")
	cols, err := p.profileAndStore(ctx, handle, ds, localParquetPath)
	if err != nil {
		p.fail(ctx, ds, err.Error())
		return
	}

	if err := p.inferRelationshipsAndComplete(ctx, handle, ds, localParquetPath, cols); err != nil {
		p.fail(ctx, ds, err.Error())
		return
	}

	p.tracker.Clear(ds.ID)
	p.logger.Info("ingest: complete",
		slog.String("dataset_id", ds.ID),
		slog.String("workspace_id", ds.WorkspaceID),
		slog.Int64("row_count", rows),
		slog.Int("column_count", len(cols)),
	)
}

// profileAndStore runs FEAT-005's profiling over the dataset's normalized Parquet and persists the
// resulting Column rows, returning them so relationship inference can use them without a re-fetch.
func (p *Pipeline) profileAndStore(ctx context.Context, handle engine.Handle, ds domain.Dataset, localParquetPath string) ([]domain.Column, error) {
	stats, err := profiling.Profile(ctx, handle, localParquetPath)
	if err != nil {
		p.logger.Error("ingest: profiling", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()))
		return nil, errors.New("could not profile the uploaded data. Try again.")
	}

	cols := make([]domain.Column, 0, len(stats))
	for _, s := range stats {
		cols = append(cols, domain.Column{
			ID:             uuid.NewString(),
			DatasetID:      ds.ID,
			Name:           s.Name,
			InferredType:   s.InferredType,
			TypeConfidence: s.TypeConfidence,
			// PII detection (FEAT-007) is a separate feature, out of this pipeline's scope; every
			// column starts unflagged and unexcluded here until FEAT-007 lands.
			PIIFlag:           false,
			PIIPattern:        "",
			ExcludedFromModel: false,
			NullRate:          s.NullRate,
			DistinctCount:     s.DistinctCount,
		})
	}

	if err := p.columns.BulkInsert(ctx, ds.WorkspaceID, cols); err != nil {
		p.logger.Error("ingest: storing columns", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()))
		return nil, errors.New("profiling finished but could not be recorded. Try again.")
	}
	return cols, nil
}

// inferRelationshipsAndComplete runs FEAT-008's relationship inference against every already-complete
// sibling dataset in the workspace (skipped entirely for a lone file — screens/06's "if 2+ files"
// condition), then marks ds "complete". The sibling lookup, inference, and completion are performed
// under this workspace's lock (see wsLocks's doc comment) so a concurrent sibling finishing at the
// same moment is never missed.
func (p *Pipeline) inferRelationshipsAndComplete(ctx context.Context, handle engine.Handle, ds domain.Dataset, localParquetPath string, cols []domain.Column) error {
	lock := p.workspaceLock(ds.WorkspaceID)
	lock.Lock()
	defer lock.Unlock()

	siblings, err := p.datasets.ListByWorkspace(ctx, ds.WorkspaceID)
	if err != nil {
		p.logger.Error("ingest: listing workspace datasets for relationship inference", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()))
		return errors.New("could not check for related files. Try again.")
	}

	var completedSiblings []domain.Dataset
	for _, sib := range siblings {
		if sib.ID != ds.ID && sib.IngestStatus == domain.IngestComplete {
			completedSiblings = append(completedSiblings, sib)
		}
	}

	if len(completedSiblings) > 0 {
		p.tracker.Set(ds.ID, StepRelationships, "")
		thisInput := relationships.DatasetInput{
			DatasetID: ds.ID, ParquetPath: localParquetPath, RowCount: ds.RowCount, Columns: cols,
		}

		for _, sib := range completedSiblings {
			if err := p.inferAgainstSibling(ctx, handle, thisInput, sib); err != nil {
				p.logger.Error("ingest: relationship inference",
					slog.String("dataset_id", ds.ID), slog.String("sibling_dataset_id", sib.ID), slog.String("error", err.Error()))
				return errors.New("could not check for related files. Try again.")
			}
		}
	}

	if err := p.datasets.CompleteIngest(ctx, ds.ID); err != nil {
		return errors.New("ingest finished but could not be recorded. Try again.")
	}
	return nil
}

// inferAgainstSibling downloads sib's normalized Parquet, runs relationships.Infer between it and
// thisInput, and persists every resulting candidate that doesn't already exist for that column pair.
func (p *Pipeline) inferAgainstSibling(ctx context.Context, handle engine.Handle, thisInput relationships.DatasetInput, sib domain.Dataset) error {
	sibCols, err := p.columns.ListByDataset(ctx, sib.ID)
	if err != nil {
		return fmt.Errorf("listing sibling columns: %w", err)
	}
	if len(sibCols) == 0 {
		return nil
	}

	sibPath, cleanup, err := p.downloadToTemp(ctx, sib.ParquetLocation, ".parquet")
	if err != nil {
		return fmt.Errorf("downloading sibling parquet: %w", err)
	}
	defer cleanup()

	sibInput := relationships.DatasetInput{
		DatasetID: sib.ID, ParquetPath: sibPath, RowCount: sib.RowCount, Columns: sibCols,
	}

	candidates, err := relationships.Infer(ctx, handle, thisInput, sibInput)
	if err != nil {
		return fmt.Errorf("inferring candidates: %w", err)
	}

	for _, c := range candidates {
		exists, err := p.joins.ExistsForPair(ctx, c.SourceColumnID, c.TargetColumnID)
		if err != nil {
			return fmt.Errorf("checking existing candidate join: %w", err)
		}
		if exists {
			continue
		}
		c.ID = uuid.NewString()
		c.WorkspaceID = sib.WorkspaceID
		c.CreatedAt = time.Now()
		if err := p.joins.Create(ctx, c); err != nil {
			return fmt.Errorf("storing candidate join: %w", err)
		}
	}
	return nil
}

// loadAndNormalize downloads the original upload, runs it through the engine to produce normalized
// Parquet, uploads that Parquet to its canonical location, and returns that location, the LOCAL
// filesystem path the Parquet was written to (deliberately not deleted here — profiling and
// relationship inference read it again through the engine, avoiding a redundant download), the row
// count, and a cleanup func the caller must defer once every step needing the local file has
// finished. Every returned error is already a specific, actionable message.
func (p *Pipeline) loadAndNormalize(ctx context.Context, handle engine.Handle, ds domain.Dataset) (destKey, localPath string, rows int64, cleanup func(), err error) {
	noop := func() {}

	sourceKey := storage.DatasetKey(ds.WorkspaceID, ds.ID, ds.OriginalFilename)

	sourcePath, cleanupSource, err := p.downloadToTemp(ctx, sourceKey, filepath.Ext(ds.OriginalFilename))
	if err != nil {
		return "", "", 0, noop, errors.New("could not read the uploaded file. Try uploading it again.")
	}
	defer cleanupSource()

	parquetPath := filepath.Join(p.tempDir, "groundwork-ingest-"+ds.ID+".parquet")
	cleanupParquet := func() { os.Remove(parquetPath) }

	if err := normalizeToParquet(ctx, handle, ds.Format, sourcePath, parquetPath); err != nil {
		cleanupParquet()
		return "", "", 0, noop, err
	}

	rowN, err := rowCount(ctx, handle, parquetPath)
	if err != nil {
		cleanupParquet()
		return "", "", 0, noop, err
	}

	parquetFile, err := os.Open(parquetPath)
	if err != nil {
		cleanupParquet()
		return "", "", 0, noop, errors.New("could not read the normalized output. Try again.")
	}
	defer parquetFile.Close()

	info, err := parquetFile.Stat()
	if err != nil {
		cleanupParquet()
		return "", "", 0, noop, errors.New("could not read the normalized output. Try again.")
	}

	dest := storage.NormalizedDatasetKey(ds.WorkspaceID, ds.ID)
	if err := p.objects.PutObject(ctx, dest, parquetFile, info.Size(), "application/octet-stream"); err != nil {
		p.logger.Error("ingest: uploading normalized parquet", slog.String("dataset_id", ds.ID), slog.String("error", err.Error()), slog.String("size", formatByteCount(info.Size())))
		cleanupParquet()
		return "", "", 0, noop, errors.New("could not store the normalized data. Try again.")
	}

	return dest, parquetPath, rowN, cleanupParquet, nil
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
		// Persisted "profiling" with no live Tracker entry means either a server restart mid-profile
		// or a dataset this process instance never started — "profiling" is the honest most-specific
		// answer available without a live entry (relationship inference, if it applies, only ever
		// runs after profiling, so this can never under-represent progress).
		return State{Step: StepProfiling, UpdatedAt: time.Now()}
	case domain.IngestComplete:
		return State{Step: StepRelationships, UpdatedAt: time.Now()}
	case domain.IngestFailed:
		return State{Step: StepFailed, UpdatedAt: time.Now()}
	default:
		return State{Step: StepProfiling, UpdatedAt: time.Now()}
	}
}
