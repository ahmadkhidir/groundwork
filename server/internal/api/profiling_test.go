package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// fakeProfilingDatasetStore is an in-memory ProfilingDatasetStore.
type fakeProfilingDatasetStore struct {
	byID map[string]domain.Dataset
}

func (f *fakeProfilingDatasetStore) GetByID(ctx context.Context, id string) (domain.Dataset, error) {
	ds, ok := f.byID[id]
	if !ok {
		return domain.Dataset{}, postgres.ErrDatasetNotFound
	}
	return ds, nil
}

// ListByWorkspace supports FEAT-008/FEAT-049's Relationship inference skipped notice, computed by
// handleListCandidateJoins from the workspace's full dataset list.
func (f *fakeProfilingDatasetStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
	var out []domain.Dataset
	for _, ds := range f.byID {
		if ds.WorkspaceID == workspaceID {
			out = append(out, ds)
		}
	}
	return out, nil
}

// fakeProfilingColumnStore is an in-memory ProfilingColumnStore.
type fakeProfilingColumnStore struct {
	byDataset map[string][]domain.Column
}

func (f *fakeProfilingColumnStore) ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error) {
	return f.byDataset[datasetID], nil
}

// fakeProfilingCandidateJoinStore is an in-memory ProfilingCandidateJoinStore.
type fakeProfilingCandidateJoinStore struct {
	byWorkspace map[string][]domain.CandidateJoin
	err         error
}

func (f *fakeProfilingCandidateJoinStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.CandidateJoin, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byWorkspace[workspaceID], nil
}

// newTestProfilingRouter builds a standalone chi.Router wired only with RegisterProfilingRoutes and
// the same authsession.RequireAuth middleware api.go's protected group uses — this exercises the
// handlers end to end (including the ownership boundary) without needing api.go's Dependencies
// struct extended, which is the orchestrator's job at assembly time (see this wave's report).
func newTestProfilingRouter(workspaces WorkspaceStore, datasets ProfilingDatasetStore, columns ProfilingColumnStore, joins ProfilingCandidateJoinStore) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterProfilingRoutes(r, workspaces, datasets, columns, joins, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

func TestListColumnsRequiresWorkspaceOwnership(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}
	datasets := &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{
		"ds-1": {ID: "ds-1", WorkspaceID: "ws-1"},
	}}
	router := newTestProfilingRouter(workspaces, datasets, &fakeProfilingColumnStore{}, &fakeProfilingCandidateJoinStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets/ds-1/columns", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
}

func TestListColumnsRejectsADatasetFromADifferentWorkspace(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	// ds-1 actually belongs to ws-OTHER, which this caller does not own.
	datasets := &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{
		"ds-1": {ID: "ds-1", WorkspaceID: "ws-other"},
	}}
	router := newTestProfilingRouter(workspaces, datasets, &fakeProfilingColumnStore{}, &fakeProfilingCandidateJoinStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets/ds-1/columns", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a dataset belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
}

func TestListColumnsReturnsProfiledStatistics(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{
		"ds-1": {ID: "ds-1", WorkspaceID: "ws-1"},
	}}
	columns := &fakeProfilingColumnStore{byDataset: map[string][]domain.Column{
		"ds-1": {
			{
				ID: "col-1", DatasetID: "ds-1", Name: "total", InferredType: domain.TypeNumber,
				TypeConfidence: 0.92, NullRate: 0.1, DistinctCount: 42,
			},
		},
	}}
	router := newTestProfilingRouter(workspaces, datasets, columns, &fakeProfilingCandidateJoinStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets/ds-1/columns", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Columns []publicColumn `json:"columns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Columns) != 1 {
		t.Fatalf("got %d columns, want 1", len(body.Columns))
	}
	got := body.Columns[0]
	if got.Name != "total" || got.InferredType != string(domain.TypeNumber) || got.DistinctCount != 42 {
		t.Errorf("unexpected column payload: %+v", got)
	}
}

func TestListCandidateJoinsRequiresWorkspaceOwnership(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}
	router := newTestProfilingRouter(workspaces, &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{}}, &fakeProfilingColumnStore{}, &fakeProfilingCandidateJoinStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/candidate-joins", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
}

func TestListCandidateJoinsReturnsPendingCandidatesNeverAutoApplied(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	joins := &fakeProfilingCandidateJoinStore{byWorkspace: map[string][]domain.CandidateJoin{
		"ws-1": {
			{
				ID: "cj-1", WorkspaceID: "ws-1", SourceColumnID: "col-a", TargetColumnID: "col-b",
				ConfidenceScore: 0.81, Basis: "matched column names; 90% sampled value overlap",
				Status: domain.JoinPending, CreatedAt: time.Now(),
			},
		},
	}}
	router := newTestProfilingRouter(workspaces, &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{}}, &fakeProfilingColumnStore{}, joins)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/candidate-joins", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		CandidateJoins []publicCandidateJoin `json:"candidate_joins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.CandidateJoins) != 1 {
		t.Fatalf("got %d candidate joins, want 1", len(body.CandidateJoins))
	}
	got := body.CandidateJoins[0]
	if got.Status != string(domain.JoinPending) {
		t.Errorf("status = %q, want %q — never auto-applied", got.Status, domain.JoinPending)
	}
	if got.Basis == "" {
		t.Error("basis is empty — a confidence number alone is not an explanation")
	}
}

// TestListCandidateJoinsSurfacesSkippedFilenamePairs is FEAT-008/FEAT-049's read-side test: two
// datasets sharing an original_filename (the "Keep both" outcome) must be named in a skipped_pairs
// notice, per screens/07-ingest-confirmation.md's Relationships step — not left as an unexplained
// absence of candidates.
func TestListCandidateJoinsSurfacesSkippedFilenamePairs(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{
		"ds-a": {ID: "ds-a", WorkspaceID: "ws-1", OriginalFilename: "customers.csv", CreatedAt: time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)},
		"ds-b": {ID: "ds-b", WorkspaceID: "ws-1", OriginalFilename: "customers.csv", CreatedAt: time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)},
		"ds-c": {ID: "ds-c", WorkspaceID: "ws-1", OriginalFilename: "orders.csv", CreatedAt: time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)},
	}}
	router := newTestProfilingRouter(workspaces, datasets, &fakeProfilingColumnStore{}, &fakeProfilingCandidateJoinStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/candidate-joins", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		SkippedPairs []skippedFilenamePair `json:"skipped_pairs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.SkippedPairs) != 1 {
		t.Fatalf("skipped_pairs = %+v, want exactly one group (customers.csv) — orders.csv has no collision", body.SkippedPairs)
	}
	pair := body.SkippedPairs[0]
	if pair.Filename != "customers.csv" {
		t.Errorf("skipped pair filename = %q, want customers.csv", pair.Filename)
	}
	if len(pair.Datasets) != 2 {
		t.Fatalf("skipped pair datasets = %+v, want both ds-a and ds-b named", pair.Datasets)
	}
}

func TestListColumnsSurfacesInternalErrorHonestly(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestProfilingRouter(workspaces, &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{}}, &fakeProfilingColumnStore{}, &fakeProfilingCandidateJoinStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets/ds-missing/columns", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a dataset that does not exist", rec.Code, http.StatusNotFound)
	}
}

func TestListCandidateJoinsSurfacesStoreErrorAs500(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	joins := &fakeProfilingCandidateJoinStore{err: errors.New("db unreachable")}
	router := newTestProfilingRouter(workspaces, &fakeProfilingDatasetStore{byID: map[string]domain.Dataset{}}, &fakeProfilingColumnStore{}, joins)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/candidate-joins", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d on a store error", rec.Code, http.StatusInternalServerError)
	}
}
