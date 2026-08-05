package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDatasetStore is an in-memory DatasetStore recording every state transition.
type fakeDatasetStore struct {
	mu   sync.Mutex
	byID map[string]domain.Dataset
}

func newFakeDatasetStore(datasets ...domain.Dataset) *fakeDatasetStore {
	f := &fakeDatasetStore{byID: make(map[string]domain.Dataset)}
	for _, ds := range datasets {
		f.byID[ds.ID] = ds
	}
	return f
}

func (f *fakeDatasetStore) GetByID(ctx context.Context, id string) (domain.Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds, ok := f.byID[id]
	if !ok {
		return domain.Dataset{}, errors.New("not found")
	}
	return ds, nil
}

func (f *fakeDatasetStore) SetIngestStatus(ctx context.Context, id string, status domain.IngestStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds := f.byID[id]
	ds.IngestStatus = status
	f.byID[id] = ds
	return nil
}

func (f *fakeDatasetStore) CompleteLoad(ctx context.Context, id, parquetLocation string, rowCount int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds := f.byID[id]
	ds.ParquetLocation = parquetLocation
	ds.RowCount = rowCount
	ds.IngestStatus = domain.IngestProfiling
	f.byID[id] = ds
	return nil
}

func (f *fakeDatasetStore) MarkFailed(ctx context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds := f.byID[id]
	ds.IngestStatus = domain.IngestFailed
	f.byID[id] = ds
	return nil
}

func (f *fakeDatasetStore) get(id string) domain.Dataset {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byID[id]
}

// fakeObjectStore is an in-memory ObjectStore.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: make(map[string][]byte)}
}

func (f *fakeObjectStore) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, errors.New("object not found: " + key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeObjectStore) PutObject(ctx context.Context, key string, content io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
	return nil
}

// engineProviderAdapter adapts engine.LocalProvider to this package's narrower EngineProvider seam.
type engineProviderAdapter struct {
	*engine.LocalProvider
}

func waitForTerminalStatus(t *testing.T, store *fakeDatasetStore, datasetID string) domain.Dataset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ds := store.get(datasetID)
		if ds.IngestStatus == domain.IngestProfiling || ds.IngestStatus == domain.IngestFailed {
			return ds
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ingest never reached a terminal state (profiling or failed)")
	return domain.Dataset{}
}

func TestPipelineIngestsCSVSuccessfully(t *testing.T) {
	ws, dsID := "ws-1", "ds-1"
	ds := domain.Dataset{
		ID: dsID, WorkspaceID: ws, OriginalFilename: "orders.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}

	datasets := newFakeDatasetStore(ds)
	objects := newFakeObjectStore()
	objects.objects["workspaces/ws-1/datasets/ds-1/original/orders.csv"] = []byte("id,total\n1,42.5\n2,10\n3,7.25\n")

	provider := engine.NewLocalProvider(time.Minute, testLogger())
	tracker := NewTracker()
	pipeline := NewPipeline(datasets, objects, engineProviderAdapter{provider}, tracker, testLogger())

	pipeline.Start(dsID)

	final := waitForTerminalStatus(t, datasets, dsID)
	if final.IngestStatus != domain.IngestProfiling {
		t.Fatalf("ingest_status = %q, want %q; check tracker error: %+v", final.IngestStatus, domain.IngestProfiling, tracker.states)
	}
	if final.RowCount != 3 {
		t.Errorf("row_count = %d, want 3", final.RowCount)
	}
	if final.ParquetLocation == "" {
		t.Error("parquet_location is empty, want the normalized object key")
	}
	if _, ok := objects.objects[final.ParquetLocation]; !ok {
		t.Errorf("normalized parquet was not written to %q", final.ParquetLocation)
	}

	// This wave hands off at "profiling" without asserting further live progress.
	if _, ok := tracker.Get(dsID); ok {
		t.Error("tracker entry should be cleared once handed off to profiling")
	}
}

func TestPipelineSurfacesSpecificErrorOnMalformedCSV(t *testing.T) {
	ws, dsID := "ws-1", "ds-bad"
	ds := domain.Dataset{
		ID: dsID, WorkspaceID: ws, OriginalFilename: "bad.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}

	datasets := newFakeDatasetStore(ds)
	objects := newFakeObjectStore()
	// Invalid UTF-8 bytes reliably fail DuckDB's CSV reader outright — unlike a ragged row count or
	// an unterminated quote, both of which auto_detect resolved leniently when probed against this
	// driver. This is also a direct match for FEAT-004's own example: "unsupported encoding."
	objects.objects["workspaces/ws-1/datasets/ds-bad/original/bad.csv"] = []byte(
		"id,total\n1,\xc0\x80bad\n2,10\n",
	)

	provider := engine.NewLocalProvider(time.Minute, testLogger())
	tracker := NewTracker()
	pipeline := NewPipeline(datasets, objects, engineProviderAdapter{provider}, tracker, testLogger())

	pipeline.Start(dsID)

	final := waitForTerminalStatus(t, datasets, dsID)
	if final.IngestStatus != domain.IngestFailed {
		t.Fatalf("ingest_status = %q, want %q", final.IngestStatus, domain.IngestFailed)
	}

	state, ok := tracker.Get(dsID)
	if !ok {
		t.Fatal("expected a tracker entry describing the failure")
	}
	if state.Step != StepFailed {
		t.Errorf("step = %q, want %q", state.Step, StepFailed)
	}
	if state.Error == "" {
		t.Error("expected a specific, non-empty failure reason")
	}
}

func TestPipelineStartIsIdempotentForNonPendingDatasets(t *testing.T) {
	ds := domain.Dataset{
		ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "orders.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestComplete, // already terminal
	}
	datasets := newFakeDatasetStore(ds)
	objects := newFakeObjectStore() // deliberately has no object for this dataset
	provider := engine.NewLocalProvider(time.Minute, testLogger())
	tracker := NewTracker()
	pipeline := NewPipeline(datasets, objects, engineProviderAdapter{provider}, tracker, testLogger())

	pipeline.Start(ds.ID)

	// Give any (incorrectly started) goroutine a moment to run, then assert nothing changed.
	time.Sleep(100 * time.Millisecond)
	if got := datasets.get(ds.ID).IngestStatus; got != domain.IngestComplete {
		t.Errorf("ingest_status changed to %q; Start should have been a no-op for a non-pending dataset", got)
	}
}

func TestStateForFallsBackToPersistedStatusWithoutATrackerEntry(t *testing.T) {
	tracker := NewTracker()

	cases := []struct {
		status domain.IngestStatus
		want   Step
	}{
		{domain.IngestPending, StepProvisioning},
		{domain.IngestProvisioning, StepProvisioning},
		{domain.IngestProfiling, StepProfiling},
		{domain.IngestFailed, StepFailed},
	}
	for _, c := range cases {
		ds := domain.Dataset{ID: "ds-x", IngestStatus: c.status}
		got := StateFor(tracker, ds)
		if got.Step != c.want {
			t.Errorf("StateFor(%q) = %q, want %q", c.status, got.Step, c.want)
		}
	}
}
