package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// fakeDatasetsStore is an in-memory DatasetsStore (list/get/delete), for FEAT-048's tests.
type fakeDatasetsStore struct {
	mu   sync.Mutex
	byID map[string]domain.Dataset
}

func newFakeDatasetsStore(datasets ...domain.Dataset) *fakeDatasetsStore {
	f := &fakeDatasetsStore{byID: make(map[string]domain.Dataset)}
	for _, ds := range datasets {
		f.byID[ds.ID] = ds
	}
	return f
}

func (f *fakeDatasetsStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
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

func (f *fakeDatasetsStore) GetByID(ctx context.Context, id string) (domain.Dataset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ds, ok := f.byID[id]
	if !ok {
		return domain.Dataset{}, postgres.ErrDatasetNotFound
	}
	return ds, nil
}

func (f *fakeDatasetsStore) DeleteByID(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
	return nil
}

func (f *fakeDatasetsStore) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.byID[id]
	return ok
}

// fakeDatasetColumnsStore is an in-memory DatasetColumnsStore.
type fakeDatasetColumnsStore struct {
	byDataset map[string][]domain.Column
}

func (f *fakeDatasetColumnsStore) ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error) {
	return f.byDataset[datasetID], nil
}

// fakeDatasetRelationshipsStore is an in-memory DatasetRelationshipsStore.
type fakeDatasetRelationshipsStore struct {
	byDataset map[string][]postgres.DatasetRelationship
}

func (f *fakeDatasetRelationshipsStore) ListByDatasetColumns(ctx context.Context, workspaceID, datasetID string) ([]postgres.DatasetRelationship, error) {
	return f.byDataset[datasetID], nil
}

// fakeDatasetObjectDeleter is an in-memory DatasetObjectDeleter/ObjectStore recording every deleted
// key, and (via failKey) simulating a storage failure for a specific key.
type fakeDatasetObjectDeleter struct {
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
	failKey string
}

func newFakeDatasetObjectDeleter() *fakeDatasetObjectDeleter {
	return &fakeDatasetObjectDeleter{objects: make(map[string][]byte)}
}

func (f *fakeDatasetObjectDeleter) DeleteObject(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKey != "" && key == f.failKey {
		return errors.New("simulated storage failure")
	}
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeDatasetObjectDeleter) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// fakeDatasetDeletionAuditWriter is an in-memory DatasetDeletionAuditWriter.
type fakeDatasetDeletionAuditWriter struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (f *fakeDatasetDeletionAuditWriter) WriteDeletion(ctx context.Context, workspaceID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("simulated audit failure")
	}
	f.calls++
	return nil
}

func (f *fakeDatasetDeletionAuditWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTestDatasetsRouter builds a standalone chi.Router wired only with RegisterDatasetRoutes, mirroring
// newTestProfilingRouter/newTestConfirmationRouter's pattern for exercising these handlers (including
// the ownership boundary) without api.go's full Dependencies struct.
func newTestDatasetsRouter(
	workspaces WorkspaceStore,
	datasets DatasetsStore,
	columns DatasetColumnsStore,
	relationships DatasetRelationshipsStore,
	objects DatasetObjectDeleter,
	auditWriter DatasetDeletionAuditWriter,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterDatasetRoutes(r, workspaces, datasets, columns, relationships, objects, auditWriter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

func TestListDatasetsRequiresWorkspaceOwnership(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}
	router := newTestDatasetsRouter(workspaces, newFakeDatasetsStore(), &fakeDatasetColumnsStore{}, &fakeDatasetRelationshipsStore{}, newFakeDatasetObjectDeleter(), &fakeDatasetDeletionAuditWriter{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
}

func TestListDatasetsReturnsWorkspaceDatasets(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := newFakeDatasetsStore(
		domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "a.csv", IngestStatus: domain.IngestComplete, RowCount: 10},
		domain.Dataset{ID: "ds-2", WorkspaceID: "ws-other", OriginalFilename: "b.csv"},
	)
	router := newTestDatasetsRouter(workspaces, datasets, &fakeDatasetColumnsStore{}, &fakeDatasetRelationshipsStore{}, newFakeDatasetObjectDeleter(), &fakeDatasetDeletionAuditWriter{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Datasets []publicDatasetSummary `json:"datasets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Datasets) != 1 || body.Datasets[0].ID != "ds-1" {
		t.Fatalf("datasets = %+v, want exactly ds-1 (ws-other's dataset must not leak)", body.Datasets)
	}
}

// TestDatasetDeletionImpactNamesDependentRelationshipsAndReviewedPII exercises FEAT-048's disclosure
// requirement directly: the confirmation payload must name a dependent Relationship, not merely
// signal "something depends on this," and must precisely report whether any column was PII-reviewed.
func TestDatasetDeletionImpactNamesDependentRelationshipsAndReviewedPII(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := newFakeDatasetsStore(domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "customers.csv"})
	columns := &fakeDatasetColumnsStore{byDataset: map[string][]domain.Column{
		"ds-1": {
			{ID: "col-1", DatasetID: "ds-1", Name: "customer_id"},
			{ID: "col-2", DatasetID: "ds-1", Name: "email", PIIFlag: false, PIIReviewedAt: "2026-08-01T00:00:00Z"},
		},
	}}
	relationships := &fakeDatasetRelationshipsStore{byDataset: map[string][]postgres.DatasetRelationship{
		"ds-1": {{
			Relationship:     domain.Relationship{ID: "rel-1", WorkspaceID: "ws-1", SourceColumnID: "col-1", TargetColumnID: "orders-col"},
			SourceColumnName: "customer_id",
			TargetColumnName: "customer_id",
		}},
	}}
	router := newTestDatasetsRouter(workspaces, datasets, columns, relationships, newFakeDatasetObjectDeleter(), &fakeDatasetDeletionAuditWriter{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/datasets/ds-1/deletion-impact", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var impact datasetDeletionImpact
	if err := json.Unmarshal(rec.Body.Bytes(), &impact); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !impact.HasReviewedPII {
		t.Error("has_reviewed_pii = false, want true (one column has pii_reviewed_at set)")
	}
	if len(impact.DependentRelationships) != 1 || impact.DependentRelationships[0].ID != "rel-1" {
		t.Fatalf("dependent_relationships = %+v, want exactly rel-1 named", impact.DependentRelationships)
	}
	if impact.ColumnCount != 2 {
		t.Errorf("column_count = %d, want 2", impact.ColumnCount)
	}
}

// TestDeleteDatasetRemovesOriginalAndNormalizedObjects is the highest-value test for FEAT-048: the
// acceptance criterion is explicit that BOTH objects (original upload and normalized Parquet) are
// removed via an explicit call, never assumed to cascade — this asserts both keys are actually
// deleted, not merely that the DB row disappears.
func TestDeleteDatasetRemovesOriginalAndNormalizedObjects(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	ds := domain.Dataset{
		ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "customers.csv",
		ParquetLocation: "workspaces/ws-1/datasets/ds-1/normalized/data.parquet",
		IngestStatus:    domain.IngestComplete,
	}
	datasets := newFakeDatasetsStore(ds)
	objects := newFakeDatasetObjectDeleter()
	auditWriter := &fakeDatasetDeletionAuditWriter{}
	router := newTestDatasetsRouter(workspaces, datasets, &fakeDatasetColumnsStore{}, &fakeDatasetRelationshipsStore{}, objects, auditWriter)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/datasets/ds-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	deleted := objects.deletedKeys()
	wantOriginal := "workspaces/ws-1/datasets/ds-1/original/customers.csv"
	wantNormalized := "workspaces/ws-1/datasets/ds-1/normalized/data.parquet"
	foundOriginal, foundNormalized := false, false
	for _, key := range deleted {
		if key == wantOriginal {
			foundOriginal = true
		}
		if key == wantNormalized {
			foundNormalized = true
		}
	}
	if !foundOriginal {
		t.Errorf("original object %q was never deleted; deleted keys: %v", wantOriginal, deleted)
	}
	if !foundNormalized {
		t.Errorf("normalized object %q was never deleted; deleted keys: %v", wantNormalized, deleted)
	}

	if datasets.has("ds-1") {
		t.Error("dataset row still exists after deletion")
	}
	if auditWriter.count() != 1 {
		t.Errorf("audit deletion entries = %d, want 1", auditWriter.count())
	}
}

// TestDeleteDatasetSkipsNormalizedObjectWhenIngestNeverCompletedLoad ensures a dataset that failed
// before it ever produced a normalized Parquet (ParquetLocation empty) does not attempt to delete a
// key that was never written — DeleteDatasetAndObjects only deletes what could exist.
func TestDeleteDatasetSkipsNormalizedObjectWhenIngestNeverCompletedLoad(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	ds := domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "bad.csv", IngestStatus: domain.IngestFailed}
	datasets := newFakeDatasetsStore(ds)
	objects := newFakeDatasetObjectDeleter()
	router := newTestDatasetsRouter(workspaces, datasets, &fakeDatasetColumnsStore{}, &fakeDatasetRelationshipsStore{}, objects, &fakeDatasetDeletionAuditWriter{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/datasets/ds-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	deleted := objects.deletedKeys()
	if len(deleted) != 1 || deleted[0] != "workspaces/ws-1/datasets/ds-1/original/bad.csv" {
		t.Errorf("deleted keys = %v, want exactly the original object (no normalized object was ever written)", deleted)
	}
}

// TestDeleteDatasetAbortsBeforeRemovingTheRowWhenStorageFails guards the deliberate ordering in
// DeleteDatasetAndObjects: object storage is deleted BEFORE the database row precisely so a storage
// failure leaves the dataset intact and retryable, rather than a DB row already gone with orphaned
// objects and no record of which keys they were.
func TestDeleteDatasetAbortsBeforeRemovingTheRowWhenStorageFails(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	ds := domain.Dataset{ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}
	datasets := newFakeDatasetsStore(ds)
	objects := newFakeDatasetObjectDeleter()
	objects.failKey = "workspaces/ws-1/datasets/ds-1/original/customers.csv"
	auditWriter := &fakeDatasetDeletionAuditWriter{}
	router := newTestDatasetsRouter(workspaces, datasets, &fakeDatasetColumnsStore{}, &fakeDatasetRelationshipsStore{}, objects, auditWriter)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/datasets/ds-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d when storage deletion fails", rec.Code, http.StatusInternalServerError)
	}
	if !datasets.has("ds-1") {
		t.Error("dataset row was deleted despite the storage deletion failing — it should remain intact and retryable")
	}
	if auditWriter.count() != 0 {
		t.Error("a deletion audit entry was written despite the deletion failing")
	}
}

// newTestDuplicateCheckRouter wires only FEAT-049's GET /v1/workspaces/:id/files?filename= route.
func newTestDuplicateCheckRouter(workspaces WorkspaceStore, datasets DuplicateCheckStore) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterDuplicateCheckRoute(r, workspaces, datasets, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

// TestCheckFilenameDetectsAFailedDatasetAsADuplicate is the literal incident scenario FEAT-049 exists
// to prevent: a prior upload stuck at ingest_status "failed" must still trigger the duplicate prompt
// on retry, not be silently treated as available for a fresh, indistinguishable upload.
func TestCheckFilenameDetectsAFailedDatasetAsADuplicate(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := newFakeDatasetsStore(domain.Dataset{
		ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "orders.csv", IngestStatus: domain.IngestFailed,
	})
	router := newTestDuplicateCheckRouter(workspaces, datasets)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/files?filename=orders.csv", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Exists  bool                  `json:"exists"`
		Dataset *publicDatasetSummary `json:"dataset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.Exists {
		t.Fatal("exists = false, want true — a failed dataset must still be reported as a collision")
	}
	if body.Dataset == nil || body.Dataset.ID != "ds-1" {
		t.Fatalf("dataset = %+v, want ds-1", body.Dataset)
	}
	if body.Dataset.IngestStatus != string(domain.IngestFailed) {
		t.Errorf("dataset.ingest_status = %q, want %q", body.Dataset.IngestStatus, domain.IngestFailed)
	}
}

func TestCheckFilenameReportsNoCollisionForANewName(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := newFakeDatasetsStore(domain.Dataset{
		ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "orders.csv", IngestStatus: domain.IngestComplete,
	})
	router := newTestDuplicateCheckRouter(workspaces, datasets)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/files?filename=new-file.csv", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Exists {
		t.Error("exists = true for a filename with no collision, want false")
	}
}

func TestDeleteDatasetRejectsADatasetFromADifferentWorkspace(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := newFakeDatasetsStore(domain.Dataset{ID: "ds-1", WorkspaceID: "ws-OTHER", OriginalFilename: "customers.csv"})
	router := newTestDatasetsRouter(workspaces, datasets, &fakeDatasetColumnsStore{}, &fakeDatasetRelationshipsStore{}, newFakeDatasetObjectDeleter(), &fakeDatasetDeletionAuditWriter{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/datasets/ds-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a dataset belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
	if !datasets.has("ds-1") {
		t.Error("dataset from a different workspace was deleted")
	}
}
