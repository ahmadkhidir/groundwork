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

// newTestEngineProvider centralizes engine.NewLocalProvider construction for this package's tests.
// engine.NewLocalProvider's exact signature belongs to the engine package (owned by a different
// subagent in this wave, mid-rewrite) — keeping every call site funneled through this one helper
// means a signature change there is a one-line fix here rather than an edit at every test.
func newTestEngineProvider() *engine.LocalProvider {
	return engine.NewLocalProvider(time.Minute, testLogger())
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

func (f *fakeDatasetStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Dataset
	for _, ds := range f.byID {
		if ds.WorkspaceID == workspaceID {
			out = append(out, ds)
		}
	}
	return out, nil
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

func (f *fakeDatasetStore) CompleteIngest(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds := f.byID[id]
	ds.IngestStatus = domain.IngestComplete
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

// fakeColumnStore is an in-memory ColumnStore.
type fakeColumnStore struct {
	mu        sync.Mutex
	byDataset map[string][]domain.Column
}

func newFakeColumnStore() *fakeColumnStore {
	return &fakeColumnStore{byDataset: make(map[string][]domain.Column)}
}

func (f *fakeColumnStore) BulkInsert(ctx context.Context, workspaceID string, cols []domain.Column) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range cols {
		f.byDataset[c.DatasetID] = append(f.byDataset[c.DatasetID], c)
	}
	return nil
}

func (f *fakeColumnStore) ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Column(nil), f.byDataset[datasetID]...), nil
}

func (f *fakeColumnStore) countAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, cols := range f.byDataset {
		n += len(cols)
	}
	return n
}

// fakeCandidateJoinStore is an in-memory CandidateJoinStore.
type fakeCandidateJoinStore struct {
	mu    sync.Mutex
	joins []domain.CandidateJoin
}

func newFakeCandidateJoinStore() *fakeCandidateJoinStore {
	return &fakeCandidateJoinStore{}
}

func (f *fakeCandidateJoinStore) Create(ctx context.Context, cj domain.CandidateJoin) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joins = append(f.joins, cj)
	return nil
}

func (f *fakeCandidateJoinStore) ExistsForPair(ctx context.Context, columnAID, columnBID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.joins {
		if (j.SourceColumnID == columnAID && j.TargetColumnID == columnBID) ||
			(j.SourceColumnID == columnBID && j.TargetColumnID == columnAID) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeCandidateJoinStore) all() []domain.CandidateJoin {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.CandidateJoin(nil), f.joins...)
}

// engineProviderAdapter adapts engine.LocalProvider to this package's narrower EngineProvider seam.
type engineProviderAdapter struct {
	*engine.LocalProvider
}

// newTestPipeline wires a Pipeline against fresh in-memory fakes, for tests that don't need to
// inspect the fakes' contents beyond the dataset store.
func newTestPipeline(datasets *fakeDatasetStore, objects *fakeObjectStore) (*Pipeline, *fakeColumnStore, *fakeCandidateJoinStore, *Tracker) {
	provider := newTestEngineProvider()
	tracker := NewTracker()
	columns := newFakeColumnStore()
	joins := newFakeCandidateJoinStore()
	pipeline := NewPipeline(datasets, objects, engineProviderAdapter{provider}, columns, joins, tracker, testLogger())
	return pipeline, columns, joins, tracker
}

func waitForTerminalStatus(t *testing.T, store *fakeDatasetStore, datasetID string) domain.Dataset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ds := store.get(datasetID)
		if ds.IngestStatus == domain.IngestComplete || ds.IngestStatus == domain.IngestFailed {
			return ds
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ingest never reached a terminal state (complete or failed)")
	return domain.Dataset{}
}

func TestPipelineIngestsCSVSuccessfullyAndProfiles(t *testing.T) {
	ws, dsID := "ws-1", "ds-1"
	ds := domain.Dataset{
		ID: dsID, WorkspaceID: ws, OriginalFilename: "orders.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}

	datasets := newFakeDatasetStore(ds)
	objects := newFakeObjectStore()
	objects.objects["workspaces/ws-1/datasets/ds-1/original/orders.csv"] = []byte("id,total\n1,42.5\n2,10\n3,7.25\n")

	pipeline, columns, joins, tracker := newTestPipeline(datasets, objects)

	pipeline.Start(dsID)

	final := waitForTerminalStatus(t, datasets, dsID)
	if final.IngestStatus != domain.IngestComplete {
		t.Fatalf("ingest_status = %q, want %q; check tracker error: %+v", final.IngestStatus, domain.IngestComplete, tracker.states)
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

	cols, err := columns.ListByDataset(context.Background(), dsID)
	if err != nil {
		t.Fatalf("listing columns: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("got %d profiled columns, want 2 (id, total)", len(cols))
	}
	for _, c := range cols {
		if c.DatasetID != dsID {
			t.Errorf("column %q dataset_id = %q, want %q", c.Name, c.DatasetID, dsID)
		}
		if c.TypeConfidence <= 0 || c.TypeConfidence > 1 {
			t.Errorf("column %q type_confidence = %v, want in (0,1]", c.Name, c.TypeConfidence)
		}
	}

	// A single-file ingest must never trigger relationship inference.
	if len(joins.all()) != 0 {
		t.Errorf("got %d candidate joins for a single-file ingest, want 0", len(joins.all()))
	}

	if _, ok := tracker.Get(dsID); ok {
		t.Error("tracker entry should be cleared once ingest reaches a terminal state")
	}
}

func TestPipelineInfersCandidateJoinsAcrossTwoFiles(t *testing.T) {
	ws := "ws-2"
	dsA := domain.Dataset{
		ID: "ds-a", WorkspaceID: ws, OriginalFilename: "customers.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}
	dsB := domain.Dataset{
		ID: "ds-b", WorkspaceID: ws, OriginalFilename: "orders.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}

	datasets := newFakeDatasetStore(dsA, dsB)
	objects := newFakeObjectStore()
	objects.objects["workspaces/ws-2/datasets/ds-a/original/customers.csv"] = []byte(
		"customer_id,name\n1,Ada\n2,Grace\n3,Alan\n",
	)
	objects.objects["workspaces/ws-2/datasets/ds-b/original/orders.csv"] = []byte(
		"order_id,customer_id\n100,1\n101,2\n102,1\n",
	)

	pipeline, columns, joins, _ := newTestPipeline(datasets, objects)

	// Ingest sequentially rather than via Start's own goroutines: this test asserts the CONTENT of
	// inferred joins, which requires both datasets to be fully profiled deterministically; the
	// pipeline's own concurrency safety (two Start calls racing) is covered by
	// TestPipelineSerializesRelationshipInferenceAcrossConcurrentSiblings below.
	pipeline.Start(dsA.ID)
	waitForTerminalStatus(t, datasets, dsA.ID)
	pipeline.Start(dsB.ID)
	final := waitForTerminalStatus(t, datasets, dsB.ID)

	if final.IngestStatus != domain.IngestComplete {
		t.Fatalf("second dataset ingest_status = %q, want %q", final.IngestStatus, domain.IngestComplete)
	}

	found := joins.all()
	if len(found) == 0 {
		t.Fatal("expected at least one candidate join between customer_id (customers.csv) and customer_id (orders.csv)")
	}
	for _, cj := range found {
		if cj.WorkspaceID != ws {
			t.Errorf("candidate join workspace_id = %q, want %q", cj.WorkspaceID, ws)
		}
		if cj.Status != domain.JoinPending {
			t.Errorf("candidate join status = %q, want %q — never auto-applied", cj.Status, domain.JoinPending)
		}
		if cj.Basis == "" {
			t.Error("candidate join has no basis — a confidence number alone is not an explanation")
		}
		if cj.ConfidenceScore <= 0 || cj.ConfidenceScore > 1 {
			t.Errorf("confidence_score = %v, want in (0,1]", cj.ConfidenceScore)
		}
	}

	if got := columns.countAll(); got != 4 {
		t.Errorf("total profiled columns across both datasets = %d, want 4 (2 + 2)", got)
	}
}

func TestPipelineSerializesRelationshipInferenceAcrossConcurrentSiblings(t *testing.T) {
	ws := "ws-3"
	dsA := domain.Dataset{
		ID: "ds-a3", WorkspaceID: ws, OriginalFilename: "a.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}
	dsB := domain.Dataset{
		ID: "ds-b3", WorkspaceID: ws, OriginalFilename: "b.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}

	datasets := newFakeDatasetStore(dsA, dsB)
	objects := newFakeObjectStore()
	objects.objects["workspaces/ws-3/datasets/ds-a3/original/a.csv"] = []byte("shared_id,x\n1,a\n2,b\n3,c\n")
	objects.objects["workspaces/ws-3/datasets/ds-b3/original/b.csv"] = []byte("shared_id,y\n1,a\n2,b\n3,c\n")

	pipeline, _, joins, _ := newTestPipeline(datasets, objects)

	// Started concurrently, mirroring a real multi-file upload batch (handleStartIngest calls
	// Pipeline.Start once per pending dataset without waiting between calls).
	pipeline.Start(dsA.ID)
	pipeline.Start(dsB.ID)

	waitForTerminalStatus(t, datasets, dsA.ID)
	waitForTerminalStatus(t, datasets, dsB.ID)

	if len(joins.all()) == 0 {
		t.Error("expected candidate joins between the two datasets even when both finish concurrently")
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

	pipeline, _, _, tracker := newTestPipeline(datasets, objects)

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

// TestPipelineStartIsIdempotentUnderConcurrentCalls guards against the race a manual end-to-end
// verification pass actually hit: React Strict Mode double-invokes IngestProgressScreen's mount
// effect in development, firing POST /ingest twice in quick succession. Both calls read the
// dataset as still "pending" (the first goroutine hadn't yet persisted its status change), so both
// used to spawn a full concurrent run() — duplicate profiling, duplicate column rows, and a
// unique-constraint violation surfaced as a false "ingest failed."
func TestPipelineStartIsIdempotentUnderConcurrentCalls(t *testing.T) {
	ws, dsID := "ws-race", "ds-race"
	ds := domain.Dataset{
		ID: dsID, WorkspaceID: ws, OriginalFilename: "orders.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestPending,
	}
	datasets := newFakeDatasetStore(ds)
	objects := newFakeObjectStore()
	objects.objects["workspaces/ws-race/datasets/ds-race/original/orders.csv"] = []byte("id,total\n1,42.5\n2,10\n3,7.25\n")

	pipeline, columns, _, _ := newTestPipeline(datasets, objects)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pipeline.Start(dsID)
		}()
	}
	wg.Wait()

	final := waitForTerminalStatus(t, datasets, dsID)
	if final.IngestStatus != domain.IngestComplete {
		t.Fatalf("ingest_status = %q, want %q — a duplicate concurrent run corrupted the result", final.IngestStatus, domain.IngestComplete)
	}

	cols, err := columns.ListByDataset(context.Background(), dsID)
	if err != nil {
		t.Fatalf("listing columns: %v", err)
	}
	if len(cols) != 2 {
		t.Errorf("got %d profiled columns, want exactly 2 (id, total) — duplicate runs would double this", len(cols))
	}
}

func TestPipelineStartIsIdempotentForNonPendingDatasets(t *testing.T) {
	ds := domain.Dataset{
		ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "orders.csv",
		Format: domain.FormatCSV, IngestStatus: domain.IngestComplete, // already terminal
	}
	datasets := newFakeDatasetStore(ds)
	objects := newFakeObjectStore() // deliberately has no object for this dataset
	pipeline, _, _, _ := newTestPipeline(datasets, objects)

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
		{domain.IngestComplete, StepRelationships},
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
