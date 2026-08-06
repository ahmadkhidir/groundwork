package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/ingest"
)

// fakeIngestDatasetStore is an in-memory IngestDatasetStore.
type fakeIngestDatasetStore struct {
	mu   sync.Mutex
	byID map[string]domain.Dataset
}

func newFakeIngestDatasetStore(datasets ...domain.Dataset) *fakeIngestDatasetStore {
	f := &fakeIngestDatasetStore{byID: make(map[string]domain.Dataset)}
	for _, ds := range datasets {
		f.byID[ds.ID] = ds
	}
	return f
}

func (f *fakeIngestDatasetStore) ListInProgressByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Dataset
	for _, ds := range f.byID {
		if ds.WorkspaceID == workspaceID && ds.IngestStatus != domain.IngestComplete {
			out = append(out, ds)
		}
	}
	return out, nil
}

func (f *fakeIngestDatasetStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
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

func (f *fakeIngestDatasetStore) setStatus(id string, status domain.IngestStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds := f.byID[id]
	ds.IngestStatus = status
	f.byID[id] = ds
}

// fakePipeline is an in-memory IngestPipeline recording every Start call, without doing any real
// ingest work — ingest.Pipeline itself is exercised end to end in internal/ingest's own tests.
type fakePipeline struct {
	mu      sync.Mutex
	started []string
}

func (f *fakePipeline) Start(datasetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, datasetID)
}

func (f *fakePipeline) startedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}

// newTestServerForIngest builds a Server wired only with the collaborators FEAT-004's routes need,
// alongside a real fakeWorkspaceStore (defined in workspaces_test.go, same package) for ownership
// checks.
func newTestServerForIngest(t *testing.T, datasets *fakeIngestDatasetStore, pipeline *fakePipeline) (*Server, *fakeWorkspaceStore) {
	t.Helper()
	server, workspaces, _ := newTestServerForIngestWithTracker(t, datasets, pipeline)
	return server, workspaces
}

// newTestServerForIngestWithTracker also hands back the Tracker, for the tests that need to
// distinguish a failure this process is live-tracking from one left behind by an earlier session —
// which is exactly the distinction currentBatch draws.
func newTestServerForIngestWithTracker(
	t *testing.T, datasets *fakeIngestDatasetStore, pipeline *fakePipeline,
) (*Server, *fakeWorkspaceStore, *ingest.Tracker) {
	t.Helper()

	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: config.MaxUploadBytesDefault,
		Environment:    "development",
	}
	workspaces := newFakeWorkspaceStore()
	tracker := ingest.NewTracker()

	server := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Dependencies{
		AuthSecret:     testAuthSecret,
		Workspaces:     workspaces,
		IngestDatasets: datasets,
		IngestPipeline: pipeline,
		IngestTracker:  tracker,
	})
	return server, workspaces, tracker
}

func TestStartIngestRequiresWorkspaceOwnership(t *testing.T) {
	datasets := newFakeIngestDatasetStore()
	pipeline := &fakePipeline{}
	server, workspaces := newTestServerForIngest(t, datasets, pipeline)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/ingest", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
	if len(pipeline.startedIDs()) != 0 {
		t.Error("ingest must not start for a workspace the caller does not own")
	}
}

func TestStartIngestStartsEveryInProgressDataset(t *testing.T) {
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "a.csv", IngestStatus: domain.IngestPending},
		domain.Dataset{ID: "ds-2", WorkspaceID: "ws-1", OriginalFilename: "b.csv", IngestStatus: domain.IngestPending},
		domain.Dataset{ID: "ds-3", WorkspaceID: "ws-other", OriginalFilename: "c.csv", IngestStatus: domain.IngestPending},
	)
	pipeline := &fakePipeline{}
	server, workspaces := newTestServerForIngest(t, datasets, pipeline)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/ingest", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	started := pipeline.startedIDs()
	if len(started) != 2 {
		t.Fatalf("started %v, want exactly the two ws-1 datasets", started)
	}
	for _, id := range started {
		if id == "ds-3" {
			t.Error("must not start ingest for a dataset in a different workspace")
		}
	}
}

func TestIngestStreamRequiresWorkspaceOwnership(t *testing.T) {
	datasets := newFakeIngestDatasetStore()
	server, workspaces := newTestServerForIngest(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/ingest/stream", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
}

// readOneSSEEvent starts the stream handler on its own goroutine (httptest.NewRecorder doesn't
// implement http.Flusher usefully for a blocking read, so a real server + real HTTP client is used
// here) and returns the first `data: ...` frame's JSON payload.
func readOneSSEEvent(t *testing.T, srv *httptest.Server, path, userID string, tier domain.SubscriptionTier) ingestStreamEvent {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	attachSessionCookie(t, req, userID, tier)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("performing request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event ingestStreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decoding SSE event: %v", err)
		}
		return event
	}
	t.Fatal("stream closed before any event was received")
	return ingestStreamEvent{}
}

func TestIngestStreamReportsProvisioningAndNotDone(t *testing.T) {
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "a.csv", IngestStatus: domain.IngestProvisioning},
	)
	server, workspaces := newTestServerForIngest(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	if event.OverallStep != string(ingest.StepProvisioning) {
		t.Errorf("overall_step = %q, want %q", event.OverallStep, ingest.StepProvisioning)
	}
	if event.Done {
		t.Error("stream should not report done while a dataset is still provisioning")
	}
	if len(event.Datasets) != 1 || event.Datasets[0].ID != "ds-1" {
		t.Errorf("datasets = %+v, want exactly ds-1", event.Datasets)
	}
}

// TestIngestStreamNotDoneWhileProfilingOrFindingRelationships is the Wave 4 regression case for the
// bug Wave 3's handoff-based Done left behind: reaching "profiling" or "relationships" now means
// FEAT-005/FEAT-008 are genuinely running, not that this package is merely done watching. The stream
// must keep the connection open (and the client must keep waiting) through both.
func TestIngestStreamNotDoneWhileProfilingOrFindingRelationships(t *testing.T) {
	for _, status := range []domain.IngestStatus{domain.IngestProfiling} {
		datasets := newFakeIngestDatasetStore(
			domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "a.csv", IngestStatus: status},
		)
		server, workspaces := newTestServerForIngest(t, datasets, &fakePipeline{})
		workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

		httpServer := httptest.NewServer(server)
		event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
		httpServer.Close()

		if event.Done {
			t.Errorf("status %q: stream reported done, but FEAT-005/FEAT-008 have not finished yet", status)
		}
	}
}

// TestIngestStreamReportsDoneOnceEveryDatasetIsActuallyComplete is the positive half of the same
// regression: once every dataset in the workspace has reached the TRUE terminal "complete" state
// (filtered out of ListInProgressByWorkspace), the stream reports done — this is FEAT-005's real
// completion, not FEAT-004's earlier "profiling" handoff.
func TestIngestStreamReportsDoneOnceEveryDatasetIsActuallyComplete(t *testing.T) {
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "a.csv", IngestStatus: domain.IngestComplete},
	)
	server, workspaces := newTestServerForIngest(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	if !event.Done {
		t.Error("stream should report done once every dataset has actually reached ingest_status complete")
	}
	if event.OverallStep == string(ingest.StepFailed) {
		t.Error("overall_step should not be failed when nothing failed")
	}
}

// TestIngestStreamTotalDatasetsCountsTheWholeBatchNotJustInProgress guards against the bug where a
// 2-file upload's "Finding relationships" step would disappear from the client the moment one of the
// two files finished first (since it then drops out of ListInProgressByWorkspace) — total_datasets is
// computed from the full workspace history, not the in-progress subset.
func TestIngestStreamTotalDatasetsCountsTheWholeBatchNotJustInProgress(t *testing.T) {
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "a.csv", IngestStatus: domain.IngestComplete},
		domain.Dataset{ID: "ds-2", WorkspaceID: "ws-1", OriginalFilename: "b.csv", IngestStatus: domain.IngestProfiling},
	)
	server, workspaces := newTestServerForIngest(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	if event.TotalDatasets != 2 {
		t.Errorf("total_datasets = %d, want 2 (one already complete, one still profiling)", event.TotalDatasets)
	}
	if len(event.Datasets) != 1 {
		t.Errorf("datasets (in-progress only) = %d, want 1", len(event.Datasets))
	}
}

func TestIngestStreamReportsFailureWithReason(t *testing.T) {
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{
			ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "bad.csv",
			IngestStatus: domain.IngestFailed,
			IngestError:  "could not read this CSV file: unterminated quote on line 12",
		},
	)
	server, workspaces, tracker := newTestServerForIngestWithTracker(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	// A failure this process is live-tracking: the batch in front of the user, not history.
	tracker.Set("ds-1", ingest.StepFailed, "could not read this CSV file: unterminated quote on line 12")

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	if event.OverallStep != string(ingest.StepFailed) {
		t.Errorf("overall_step = %q, want %q", event.OverallStep, ingest.StepFailed)
	}
	if !event.Done {
		t.Error("stream should report done once a dataset has failed")
	}
	if len(event.Datasets) != 1 || event.Datasets[0].Error == "" {
		t.Fatalf("failed dataset must carry its reason, got %+v", event.Datasets)
	}
}

func TestIngestStreamFallsBackToThePersistedFailureReason(t *testing.T) {
	// A failure this process never tracked (server restarted mid-batch) still has its reason in the
	// database. Before this was carried on the entity, the client received an empty error and rendered
	// "Ingest failed for an unspecified reason" — a generic message standing in for a specific one,
	// which is precisely what FEAT-004's acceptance criterion forbids.
	reason := "could not read this Excel file: Failed to parse cell 'E105'"
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{
			ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "data.xlsx",
			IngestStatus: domain.IngestProfiling, IngestError: "",
		},
		domain.Dataset{
			ID: "ds-2", WorkspaceID: "ws-1", OriginalFilename: "other.xlsx",
			IngestStatus: domain.IngestFailed, IngestError: reason,
		},
	)
	server, workspaces, tracker := newTestServerForIngestWithTracker(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	// ds-2 is part of the live batch, but only its persisted row knows why it failed.
	tracker.Set("ds-2", ingest.StepFailed, "")

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	var failed *publicIngestDataset
	for i := range event.Datasets {
		if event.Datasets[i].ID == "ds-2" {
			failed = &event.Datasets[i]
		}
	}
	if failed == nil {
		t.Fatal("failed dataset missing from the stream")
	}
	if failed.Error != reason {
		t.Errorf("error = %q, want the persisted reason %q", failed.Error, reason)
	}
}

func TestIngestStreamIgnoresFailuresFromAnEarlierSession(t *testing.T) {
	// The reported bug: a user's first upload failed, and every later upload to that workspace was
	// then reported as failed the instant it started — three successful ingests in a row were shown as
	// "Ingest failed" because one terminal failure never left the in-progress list. A failed dataset
	// with no live Tracker entry is history, and must not speak for the batch in front of the user.
	datasets := newFakeIngestDatasetStore(
		domain.Dataset{
			ID: "old-failure", WorkspaceID: "ws-1", OriginalFilename: "data.xlsx",
			IngestStatus: domain.IngestFailed, IngestError: "read_xlsx: Failed to parse cell 'E105'",
		},
		domain.Dataset{
			ID: "fresh", WorkspaceID: "ws-1", OriginalFilename: "data.xlsx",
			IngestStatus: domain.IngestProfiling,
		},
	)
	server, workspaces, tracker := newTestServerForIngestWithTracker(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	tracker.Set("fresh", ingest.StepProfiling, "")

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	if event.OverallStep == string(ingest.StepFailed) {
		t.Error("a stale failure from an earlier session must not fail the current batch")
	}
	if event.Done {
		t.Error("the batch is still profiling; reporting done would skip the user past a running ingest")
	}
	for _, ds := range event.Datasets {
		if ds.ID == "old-failure" {
			t.Error("stale failed dataset should not appear in the current batch")
		}
	}
}

func TestIngestStreamReportsDoneOnceNothingIsInProgress(t *testing.T) {
	datasets := newFakeIngestDatasetStore() // nothing at all for this workspace
	server, workspaces := newTestServerForIngest(t, datasets, &fakePipeline{})
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	event := readOneSSEEvent(t, httpServer, "/v1/workspaces/ws-1/ingest/stream", "user-1", domain.TierFree)
	if !event.Done {
		t.Error("stream should report done immediately when there is nothing to ingest")
	}
}
