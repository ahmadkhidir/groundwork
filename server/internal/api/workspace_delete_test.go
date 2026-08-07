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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// seqRecorder records the order in which cascade steps run across fakes, so tests can assert the
// documented per-category ordering (including the audit-write-before-log-destruction rule).
type seqRecorder struct {
	mu    sync.Mutex
	steps []string
}

func (s *seqRecorder) add(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, step)
}

func (s *seqRecorder) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steps...)
}

// fakeWorkspaceObjectDeleter is an in-memory WorkspaceCascadeObjectDeleter recording every prefix
// swept, with (via failPrefix) a simulated storage failure.
type fakeWorkspaceObjectDeleter struct {
	mu         sync.Mutex
	prefixes   []string
	failPrefix string
	rec        *seqRecorder
}

func (f *fakeWorkspaceObjectDeleter) DeleteObjectsByPrefix(ctx context.Context, prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rec != nil {
		f.rec.add("object_store")
	}
	if f.failPrefix != "" && prefix == f.failPrefix {
		return errors.New("simulated storage failure")
	}
	f.prefixes = append(f.prefixes, prefix)
	return nil
}

func (f *fakeWorkspaceObjectDeleter) deletedPrefixes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prefixes...)
}

// fakeWorkspaceEngineDropper is an in-memory WorkspaceEngineDropper.
type fakeWorkspaceEngineDropper struct {
	mu      sync.Mutex
	dropped []string
	fail    bool
	rec     *seqRecorder
}

func (f *fakeWorkspaceEngineDropper) Drop(workspaceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rec != nil {
		f.rec.add("engine")
	}
	if f.fail {
		return errors.New("simulated engine teardown failure")
	}
	f.dropped = append(f.dropped, workspaceID)
	return nil
}

func (f *fakeWorkspaceEngineDropper) droppedWorkspaces() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dropped...)
}

// fakeWorkspaceDeletionStore is an in-memory WorkspaceDeletionStore. It shares the fakeWorkspaceStore
// so DeleteWithProof really removes the workspace row (and returns ErrWorkspaceNotFound when it is
// already gone), and it records the proof it was asked to write.
type fakeWorkspaceDeletionStore struct {
	mu         sync.Mutex
	workspaces *fakeWorkspaceStore
	proofs     *fakeDeletionProofStore
	order      []string
	failOn     map[string]error
	proof      domain.DeletionRecord
	rec        *seqRecorder
}

func newFakeWorkspaceDeletionStore(workspaces *fakeWorkspaceStore, proofs *fakeDeletionProofStore) *fakeWorkspaceDeletionStore {
	return &fakeWorkspaceDeletionStore{workspaces: workspaces, proofs: proofs, failOn: make(map[string]error)}
}

func (f *fakeWorkspaceDeletionStore) step(name string, ctx context.Context, workspaceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, name)
	if f.rec != nil {
		f.rec.add(name)
	}
	return f.failOn[name]
}

func (f *fakeWorkspaceDeletionStore) DeleteSemanticLayer(ctx context.Context, workspaceID string) error {
	return f.step("semantic_layer", ctx, workspaceID)
}

func (f *fakeWorkspaceDeletionStore) DeleteReportsAndConversation(ctx context.Context, workspaceID string) error {
	return f.step("reports_and_conversation", ctx, workspaceID)
}

func (f *fakeWorkspaceDeletionStore) DeleteQueryCaches(ctx context.Context, workspaceID string) error {
	return f.step("query_caches", ctx, workspaceID)
}

func (f *fakeWorkspaceDeletionStore) DeleteAuditLog(ctx context.Context, workspaceID string) error {
	return f.step("logs", ctx, workspaceID)
}

func (f *fakeWorkspaceDeletionStore) DeleteWithProof(ctx context.Context, workspaceID string, proof domain.DeletionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "workspace_row")
	if f.rec != nil {
		f.rec.add("workspace_row")
	}
	if err := f.failOn["workspace_row"]; err != nil {
		return err
	}
	if _, ok := f.workspaces.byID[workspaceID]; !ok {
		return postgres.ErrWorkspaceNotFound
	}
	delete(f.workspaces.byID, workspaceID)
	f.proof = proof
	if f.proofs != nil {
		f.proofs.byWS[workspaceID] = proof
	}
	return nil
}

func (f *fakeWorkspaceDeletionStore) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

func (f *fakeWorkspaceDeletionStore) writtenProof() domain.DeletionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proof
}

// fakeDeletionProofStore is an in-memory DeletionProofStore.
type fakeDeletionProofStore struct {
	mu   sync.Mutex
	byWS map[string]domain.DeletionRecord
}

func newFakeDeletionProofStore() *fakeDeletionProofStore {
	return &fakeDeletionProofStore{byWS: make(map[string]domain.DeletionRecord)}
}

func (f *fakeDeletionProofStore) GetByWorkspace(ctx context.Context, workspaceID string) (domain.DeletionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.byWS[workspaceID]
	if !ok {
		return domain.DeletionRecord{}, postgres.ErrDeletionRecordNotFound
	}
	return rec, nil
}

// fakeWorkspaceAuditWriter is an in-memory WorkspaceDeletionAuditWriter recording the call into the
// shared sequence (so tests can assert the audit entry is written before the log rows are destroyed).
type fakeWorkspaceAuditWriter struct {
	mu    sync.Mutex
	calls int
	fail  bool
	rec   *seqRecorder
}

func (f *fakeWorkspaceAuditWriter) WriteDeletion(ctx context.Context, workspaceID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rec != nil {
		f.rec.add("audit_write")
	}
	if f.fail {
		return errors.New("simulated audit failure")
	}
	f.calls++
	return nil
}

func (f *fakeWorkspaceAuditWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTestWorkspaceDeleteRouter wires only FEAT-043's routes, mirroring newTestDatasetsRouter's
// pattern so these handlers (including the ownership boundary) are exercised without api.go's full
// Dependencies struct.
func newTestWorkspaceDeleteRouter(
	workspaces WorkspaceStore,
	objects WorkspaceCascadeObjectDeleter,
	engine WorkspaceEngineDropper,
	cascade WorkspaceDeletionStore,
	proofs DeletionProofStore,
	auditWriter WorkspaceDeletionAuditWriter,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterWorkspaceDeleteRoute(r, workspaces, objects, engine, cascade, proofs, auditWriter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

// newWorkspaceDeleteFixture builds the standard happy-path fixture: ws-1 owned by user-1, all fakes
// wired, and a router. The recorder is returned so a test can assert ordering.
func newWorkspaceDeleteFixture(t *testing.T) (http.Handler, *fakeWorkspaceStore, *fakeWorkspaceObjectDeleter, *fakeWorkspaceEngineDropper, *fakeWorkspaceDeletionStore, *fakeDeletionProofStore, *fakeWorkspaceAuditWriter, *seqRecorder) {
	t.Helper()
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1", Name: "Q3 Numbers"}

	rec := &seqRecorder{}
	objects := &fakeWorkspaceObjectDeleter{rec: rec}
	engine := &fakeWorkspaceEngineDropper{rec: rec}
	proofs := newFakeDeletionProofStore()
	cascade := newFakeWorkspaceDeletionStore(workspaces, proofs)
	cascade.rec = rec
	audit := &fakeWorkspaceAuditWriter{rec: rec}

	router := newTestWorkspaceDeleteRouter(workspaces, objects, engine, cascade, proofs, audit)
	return router, workspaces, objects, engine, cascade, proofs, audit, rec
}

func TestDeleteWorkspaceRejectsNonOwner(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	audit := &fakeWorkspaceAuditWriter{}
	router := newTestWorkspaceDeleteRouter(workspaces, &fakeWorkspaceObjectDeleter{}, &fakeWorkspaceEngineDropper{}, cascade, newFakeDeletionProofStore(), audit)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
	if len(cascade.callOrder()) != 0 {
		t.Errorf("cascade ran for a non-owner: %v", cascade.callOrder())
	}
	if audit.count() != 0 {
		t.Error("audit deletion entry written for a non-owner")
	}
	if _, exists := workspaces.byID["ws-1"]; !exists {
		t.Error("workspace was deleted despite the owner gate")
	}
}

func TestDeleteWorkspaceRejectsNonexistentWorkspace(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	audit := &fakeWorkspaceAuditWriter{}
	router := newTestWorkspaceDeleteRouter(workspaces, &fakeWorkspaceObjectDeleter{}, &fakeWorkspaceEngineDropper{}, cascade, newFakeDeletionProofStore(), audit)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/does-not-exist", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a nonexistent workspace", rec.Code, http.StatusNotFound)
	}
	if len(cascade.callOrder()) != 0 {
		t.Errorf("cascade ran for a nonexistent workspace: %v", cascade.callOrder())
	}
}

// TestDeleteWorkspaceFullCascadeSuccess is FEAT-043's core acceptance test: one DELETE destroys every
// category, writes the surviving DeletionRecord proof and the audit entry, and removes the workspace
// row — with the response carrying the per-category proof the Deletion Proof screen renders.
func TestDeleteWorkspaceFullCascadeSuccess(t *testing.T) {
	router, workspaces, objects, engine, cascade, _, audit, _ := newWorkspaceDeleteFixture(t)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// The workspace row is gone, and the proof survives (recorded by the store fake).
	if _, exists := workspaces.byID["ws-1"]; exists {
		t.Error("workspace row still exists after a successful cascade")
	}

	// Object store: exactly the workspace prefix swept.
	if got := objects.deletedPrefixes(); len(got) != 1 || got[0] != "workspaces/ws-1/" {
		t.Fatalf("deleted prefixes = %v, want exactly [workspaces/ws-1/]", got)
	}

	// Engine: dropped for the workspace.
	if got := engine.droppedWorkspaces(); len(got) != 1 || got[0] != "ws-1" {
		t.Fatalf("dropped engines = %v, want exactly [ws-1]", got)
	}

	// Audit: exactly one deletion entry.
	if audit.count() != 1 {
		t.Errorf("audit deletion entries = %d, want 1", audit.count())
	}

	// Proof: the response carries it, complete with every category in canonical order.
	var body struct {
		Deletion publicDeletionRecord `json:"deletion"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Deletion.ID == "" {
		t.Error("deletion proof id is empty")
	}
	if body.Deletion.WorkspaceID != "ws-1" || body.Deletion.InitiatedBy != "user-1" {
		t.Errorf("proof = %+v, want workspace ws-1 initiated by user-1", body.Deletion)
	}
	if _, err := time.Parse(time.RFC3339, body.Deletion.CompletedAt); err != nil {
		t.Errorf("completed_at %q is not an RFC 3339 timestamp: %v", body.Deletion.CompletedAt, err)
	}
	assertDeletedCategories(t, body.Deletion.DeletedCategories)

	// The store was asked to persist exactly the same proof (in canonical order), and the GET proof
	// route can read it back — screens/14's follow-up read.
	written := cascade.writtenProof()
	writtenCats := make([]string, len(written.DeletedCategories))
	for i, c := range written.DeletedCategories {
		writtenCats[i] = string(c)
	}
	assertDeletedCategories(t, writtenCats)
	if written.ID != body.Deletion.ID {
		t.Errorf("persisted proof id %q differs from response id %q", written.ID, body.Deletion.ID)
	}
}

// publicDeletionRecord mirrors domain.DeletionRecord for JSON decoding in tests (the handler writes
// the same wire shape).
type publicDeletionRecord struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	InitiatedBy       string   `json:"initiated_by"`
	DeletedCategories []string `json:"deleted_categories"`
	CompletedAt       string   `json:"completed_at"`
}

func assertDeletedCategories(t *testing.T, got []string) {
	t.Helper()
	want := make([]string, len(domain.AllDeletedCategories))
	for i, c := range domain.AllDeletedCategories {
		want[i] = string(c)
	}
	if len(got) != len(want) {
		t.Fatalf("deleted_categories = %v, want exactly %v (the exhaustive canonical list)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deleted_categories = %v, want exactly %v (order is the documented enumeration)", got, want)
		}
	}
}

// TestDeleteWorkspaceRunsCategoriesInOrder pins the cascade's execution order — the single cascade
// "driving all categories in order" from features.md FEAT-043 — including the two orderings that
// matter for honesty: object store before any DB rows, and the audit entry BEFORE the log rows are
// destroyed.
func TestDeleteWorkspaceRunsCategoriesInOrder(t *testing.T) {
	router, _, _, _, _, _, _, rec := newWorkspaceDeleteFixture(t)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	want := []string{
		"object_store",
		"engine",
		"semantic_layer",
		"reports_and_conversation",
		"query_caches",
		"audit_write",
		"logs",
		"workspace_row",
	}
	got := rec.all()
	if len(got) != len(want) {
		t.Fatalf("cascade order = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cascade order = %v, want exactly %v (position %d: got %q, want %q)", got, want, i, got[i], want[i])
		}
	}
}

// TestDeleteWorkspacePartialFailureNamesFailedCategoryAndKeepsWorkspace is Flow 5 Alternate Path B:
// any failed category produces the explicit partial-failed response naming it, never a false success,
// and the workspace row survives so the deletion is retryable.
func TestDeleteWorkspacePartialFailureNamesFailedCategoryAndKeepsWorkspace(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	cases := []struct {
		name          string
		setup         func(objects *fakeWorkspaceObjectDeleter, engine *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore)
		wantCategory  string
		wantCompleted []string
	}{
		{
			name: "object store",
			setup: func(objects *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, _ *fakeWorkspaceDeletionStore) {
				objects.failPrefix = "workspaces/ws-1/"
			},
			wantCategory:  string(domain.CategoryObjectStore),
			wantCompleted: []string{},
		},
		{
			name: "engine",
			setup: func(_ *fakeWorkspaceObjectDeleter, engine *fakeWorkspaceEngineDropper, _ *fakeWorkspaceDeletionStore) {
				engine.fail = true
			},
			wantCategory:  "engine",
			wantCompleted: []string{string(domain.CategoryObjectStore)},
		},
		{
			name: "semantic layer",
			setup: func(_ *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore) {
				cascade.failOn["semantic_layer"] = errors.New("simulated semantic layer failure")
			},
			wantCategory:  string(domain.CategorySemanticLayer),
			wantCompleted: []string{string(domain.CategoryObjectStore)},
		},
		{
			name: "reports and conversation",
			setup: func(_ *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore) {
				cascade.failOn["reports_and_conversation"] = errors.New("simulated reports failure")
			},
			wantCategory:  string(domain.CategoryReportsAndConversation),
			wantCompleted: []string{string(domain.CategoryObjectStore), string(domain.CategorySemanticLayer)},
		},
		{
			name: "query caches",
			setup: func(_ *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore) {
				cascade.failOn["query_caches"] = errors.New("simulated cache failure")
			},
			wantCategory: string(domain.CategoryQueryCaches),
			wantCompleted: []string{
				string(domain.CategoryObjectStore),
				string(domain.CategorySemanticLayer),
				string(domain.CategoryReportsAndConversation),
			},
		},
		{
			name: "audit write",
			setup: func(_ *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore) {
				// The audit writer is separate from the cascade fake; fail it through the fixture's own
				// audit writer by rebuilding below. This case is handled separately.
			},
			wantCategory: string(domain.CategoryLogs),
			wantCompleted: []string{
				string(domain.CategoryObjectStore),
				string(domain.CategorySemanticLayer),
				string(domain.CategoryReportsAndConversation),
				string(domain.CategoryQueryCaches),
			},
		},
		{
			name: "log rows",
			setup: func(_ *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore) {
				cascade.failOn["logs"] = errors.New("simulated audit log failure")
			},
			wantCategory: string(domain.CategoryLogs),
			wantCompleted: []string{
				string(domain.CategoryObjectStore),
				string(domain.CategorySemanticLayer),
				string(domain.CategoryReportsAndConversation),
				string(domain.CategoryQueryCaches),
			},
		},
		{
			name: "workspace row",
			setup: func(_ *fakeWorkspaceObjectDeleter, _ *fakeWorkspaceEngineDropper, cascade *fakeWorkspaceDeletionStore) {
				cascade.failOn["workspace_row"] = errors.New("simulated row delete failure")
			},
			wantCategory:  "workspace_row",
			wantCompleted: []string{string(domain.CategoryObjectStore), string(domain.CategorySemanticLayer), string(domain.CategoryReportsAndConversation), string(domain.CategoryQueryCaches), string(domain.CategoryLogs)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects := &fakeWorkspaceObjectDeleter{}
			engine := &fakeWorkspaceEngineDropper{}
			proofs := newFakeDeletionProofStore()
			cascade := newFakeWorkspaceDeletionStore(workspaces, proofs)
			audit := &fakeWorkspaceAuditWriter{}

			if tc.name == "audit write" {
				audit.fail = true
			} else {
				tc.setup(objects, engine, cascade)
			}

			router := newTestWorkspaceDeleteRouter(workspaces, objects, engine, cascade, proofs, audit)
			req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d (never a false success), body: %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
			}

			var body deletionPartialFailure
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if body.Code != "deletion_partial_failed" {
				t.Errorf("code = %q, want deletion_partial_failed", body.Code)
			}
			if body.FailedCategory != tc.wantCategory {
				t.Errorf("failed_category = %q, want %q", body.FailedCategory, tc.wantCategory)
			}
			if body.Error == "" || body.Error != "The workspace was not deleted — the \""+tc.wantCategory+"\" stage of the cascade failed. Your workspace is intact; retry the deletion." {
				t.Errorf("error message = %q, want it to name the failed category", body.Error)
			}
			if !body.WorkspaceIntact {
				t.Error("workspace_intact = false — the response must state the workspace was not deleted")
			}
			if len(body.Completed) != len(tc.wantCompleted) {
				t.Fatalf("completed_categories = %v, want %v", body.Completed, tc.wantCompleted)
			}
			for i := range tc.wantCompleted {
				if body.Completed[i] != tc.wantCompleted[i] {
					t.Fatalf("completed_categories = %v, want %v", body.Completed, tc.wantCompleted)
				}
			}

			// The workspace row must survive every partial failure — retryable, never a false deletion.
			if _, exists := workspaces.byID["ws-1"]; !exists {
				t.Error("workspace row was deleted despite the partial failure — it must remain intact and retryable")
			}
			// No proof may be written for a cascade that did not complete — a false deletion record.
			if _, err := proofs.GetByWorkspace(context.Background(), "ws-1"); err == nil {
				t.Error("a deletion proof was recorded despite the cascade failing — never a false proof")
			}
		})
	}
}

// TestDeleteWorkspaceWritesAuditEntryBeforeDestroyingLogs pins the one ordering rule the prompt calls
// out explicitly: the cascade's own audit entry is written BEFORE the workspace's audit log rows are
// destroyed. If the log-destruction step ran first, the entry could never be written into a log that
// still exists — and on a later-stage failure there would be no record of the deletion attempt.
func TestDeleteWorkspaceWritesAuditEntryBeforeDestroyingLogs(t *testing.T) {
	router, _, _, _, _, _, _, rec := newWorkspaceDeleteFixture(t)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	order := rec.all()
	auditWriteIdx, logsIdx := -1, -1
	for i, step := range order {
		if step == "audit_write" {
			auditWriteIdx = i
		}
		if step == "logs" {
			logsIdx = i
		}
	}
	if auditWriteIdx == -1 || logsIdx == -1 {
		t.Fatalf("cascade order %v is missing audit_write or logs", order)
	}
	if auditWriteIdx > logsIdx {
		t.Fatalf("audit_write ran at %d after logs at %d — the deletion entry must be written before the log rows are destroyed", auditWriteIdx, logsIdx)
	}
}

// TestGetDeletionProofReturnsRecord exercises screens/14's follow-up GET: after a successful cascade,
// the proof is readable by the user who initiated the deletion.
func TestGetDeletionProofReturnsRecord(t *testing.T) {
	router, _, _, _, _, proofs, _, _ := newWorkspaceDeleteFixture(t)
	proofs.byWS["ws-1"] = domain.DeletionRecord{
		ID:                "proof-1",
		WorkspaceID:       "ws-1",
		InitiatedBy:       "user-1",
		DeletedCategories: append([]domain.DeletedCategory(nil), domain.AllDeletedCategories...),
		CompletedAt:       time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/deletion", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Deletion publicDeletionRecord `json:"deletion"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Deletion.ID != "proof-1" || body.Deletion.WorkspaceID != "ws-1" {
		t.Errorf("deletion = %+v, want proof-1 for ws-1", body.Deletion)
	}
	assertDeletedCategories(t, body.Deletion.DeletedCategories)
}

// TestGetDeletionProofRejectsNonInitiator keeps the proof read owner-scoped even though the workspace
// is gone: a user who did not initiate the deletion must not read its proof, and the 404 is
// indistinguishable from "no record" (non-enumeration).
func TestGetDeletionProofRejectsNonInitiator(t *testing.T) {
	router, _, _, _, _, proofs, _, _ := newWorkspaceDeleteFixture(t)
	proofs.byWS["ws-1"] = domain.DeletionRecord{ID: "proof-1", WorkspaceID: "ws-1", InitiatedBy: "someone-else"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/deletion", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-initiator", rec.Code, http.StatusNotFound)
	}
}

func TestGetDeletionProofNotFound(t *testing.T) {
	router, _, _, _, _, _, _, _ := newWorkspaceDeleteFixture(t)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/never-deleted/deletion", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a workspace with no deletion record", rec.Code, http.StatusNotFound)
	}
}
