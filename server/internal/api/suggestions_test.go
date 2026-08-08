package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// fakeSuggestionSource is an in-memory SuggestionSource.
type fakeSuggestionSource struct {
	datasets      []domain.Dataset
	columns       []domain.Column
	relationships []domain.Relationship
	metrics       []domain.MetricDefinition
	err           error
}

func (f *fakeSuggestionSource) Datasets(_ context.Context, _ string) ([]domain.Dataset, error) {
	return f.datasets, f.err
}
func (f *fakeSuggestionSource) Columns(_ context.Context, _ string) ([]domain.Column, error) {
	return f.columns, f.err
}
func (f *fakeSuggestionSource) Relationships(_ context.Context, _ string) ([]domain.Relationship, error) {
	return f.relationships, f.err
}
func (f *fakeSuggestionSource) Metrics(_ context.Context, _ string) ([]domain.MetricDefinition, error) {
	return f.metrics, f.err
}

// newTestSuggestionsRouter mirrors RegisterSuggestionRoutes' wiring without api.go's Dependencies.
// engine/model/objects nil by default — the smart route is registered only when all are wired.
func newTestSuggestionsRouter(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource) http.Handler {
	return newTestSuggestionsRouterWithSmart(workspaces, members, source, &fakeSuggestionBucket{}, nil, nil, nil, newFakeUserTierLookup())
}

func newTestSuggestionsRouterWithSmart(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource, bucket SuggestionBucket, engineProvider SuggestionEngineProvider, modelClient SmartSuggestionModel, objects ObjectGetter, users UserTierLookup) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			RegisterSuggestionRoutes(r, workspaces, members, users, source, bucket, engineProvider, modelClient, objects, slog.New(slog.NewTextHandler(io.Discard, nil)))
		})
	})
	return r
}

// fakeSuggestionBucket is an in-memory SuggestionBucket mirroring the postgres contract: Add
// swallows exact duplicates and returns the full bucket, List returns the stored rows, Delete
// removes by (workspace, id) and returns the not-found sentinel otherwise. addCalls records the
// exact questions each Add received, so tests pin the handler's dedupe before persistence.
type fakeSuggestionBucket struct {
	rows     []domain.SuggestionQuestion
	addCalls [][]string
	err      error
}

func (f *fakeSuggestionBucket) ListByWorkspace(_ context.Context, _ string) ([]domain.SuggestionQuestion, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.SuggestionQuestion, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeSuggestionBucket) Add(_ context.Context, workspaceID string, questions []string) ([]domain.SuggestionQuestion, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.addCalls = append(f.addCalls, questions)
	existing := make(map[string]bool, len(f.rows))
	for _, r := range f.rows {
		existing[r.Question] = true
	}
	for _, q := range questions {
		if q == "" || existing[q] {
			continue
		}
		f.rows = append(f.rows, domain.SuggestionQuestion{
			ID:          fmt.Sprintf("sq-%d", len(f.rows)+1),
			WorkspaceID: workspaceID,
			Question:    q,
			CreatedAt:   time.Now(),
		})
		existing[q] = true
	}
	out := make([]domain.SuggestionQuestion, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeSuggestionBucket) Delete(_ context.Context, workspaceID, id string) error {
	for i, r := range f.rows {
		if r.ID == id && r.WorkspaceID == workspaceID {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return postgres.ErrSuggestionQuestionNotFound
}

// fakeObjectGetter returns a dummy reader for any object key.
type fakeObjectGetter struct{}

func (f *fakeObjectGetter) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("dummy parquet bytes")), nil
}

func TestListSuggestionsServesDeterministicMetadataOnlyQuestions(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	src := &fakeSuggestionSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			{ID: "c-1", DatasetID: "ds-1", Name: "total_amount", InferredType: domain.TypeNumber},
			{ID: "c-2", DatasetID: "ds-1", Name: "customer_id", InferredType: domain.TypeString},
		},
	}
	router := newTestSuggestionsRouter(workspaces, newFakeMemberLookupStore(), src)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/suggestions", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Suggestions) < 5 {
		t.Errorf("got %d suggestions, want at least 5 for a data-rich workspace: %v", len(body.Suggestions), body.Suggestions)
	}
	for _, s := range body.Suggestions {
		if s == "" {
			t.Error("empty suggestion in response")
		}
	}
}

func TestListSuggestionsRequiresWorkspaceAccess(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestSuggestionsRouter(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{})

	// A user who is neither owner nor member gets the non-enumeration 404.
	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/suggestions", nil, "user-2", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a non-member; body: %s", rec.Code, rec.Body.String())
	}
}

func TestListSuggestionsSurfacesInternalError(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestSuggestionsRouter(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{err: errors.New("boom")})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/suggestions", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
}

// ---- FEAT-051 smart generation ----------------------------------------------------------------

// fakeSmartEngine is an in-memory SuggestionEngineProvider that records acquire/release.
type fakeSmartEngine struct {
	acquires int
	releases int
	err      error
}

type fakeHandle struct{}

func (f *fakeHandle) WorkspaceID() string { return "ws-1" }
func (f *fakeHandle) Query(_ context.Context, _ string, _ ...any) (engine.Rows, error) {
	return &fakeEngineRows{}, nil
}
func (f *fakeHandle) Exec(_ context.Context, _ string, _ ...any) (int64, error) { return 0, nil }
func (f *fakeHandle) AcquiredAt() time.Time                                     { return time.Now() }

type fakeEngineRows struct{}

func (f *fakeEngineRows) Next() bool          { return false }
func (f *fakeEngineRows) Scan(_ ...any) error { return nil }
func (f *fakeEngineRows) Columns() []string   { return nil }
func (f *fakeEngineRows) Err() error          { return nil }
func (f *fakeEngineRows) Close() error        { return nil }

func (f *fakeSmartEngine) Acquire(_ context.Context, _ string) (engine.Handle, error) {
	f.acquires++
	if f.err != nil {
		return nil, f.err
	}
	return &fakeHandle{}, nil
}
func (f *fakeSmartEngine) Release(_ engine.Handle) error {
	f.releases++
	return nil
}

// fakeSmartModel records the payload it receives and returns canned output.
type fakeSmartModel struct {
	payloads []model.Payload
	text     string
	err      error
}

func (f *fakeSmartModel) Complete(_ context.Context, _, _ string, _ string, payload model.Payload) (string, error) {
	f.payloads = append(f.payloads, payload)
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

func TestSmartSuggestionsGeneratesAndPersistsForProUser(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	engineProvider := &fakeSmartEngine{}
	// Both questions reference known safe columns (total_amount, customer_id) so ParseModelResponse
	// accepts them, and neither is a deterministic candidate for the fixture below — both append.
	modelClient := &fakeSmartModel{text: `["Which customer id has the highest total amount?", "How does total amount vary by customer id?"]`}
	src := &fakeSuggestionSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			{ID: "c-1", DatasetID: "ds-1", Name: "total_amount", InferredType: domain.TypeNumber},
			{ID: "c-2", DatasetID: "ds-1", Name: "customer_id", InferredType: domain.TypeString},
		},
	}
	bucket := &fakeSuggestionBucket{}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), src, bucket, engineProvider, modelClient, &fakeObjectGetter{}, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Suggestions    []string                   `json:"suggestions"`
		Smart          []publicSuggestionQuestion `json:"smart"`
		ModelGenerated bool                       `json:"model_generated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if !body.ModelGenerated {
		t.Error("model_generated = false, want true")
	}
	// FEAT-051 "add to the list": the model's questions are appended to the bucket and returned
	// under `smart`; the deterministic set is returned unchanged under `suggestions`.
	if len(body.Smart) != 2 {
		t.Errorf("smart = %+v, want both model questions appended", body.Smart)
	}
	if len(bucket.rows) != 2 {
		t.Errorf("bucket rows = %d, want 2 persisted", len(bucket.rows))
	}
	if body.Suggestions == nil || len(body.Suggestions) < 5 {
		t.Errorf("deterministic suggestions = %v, want the standard strip present", body.Suggestions)
	}
	// The engine was leased for profiling and returned.
	if engineProvider.acquires != 1 || engineProvider.releases != 1 {
		t.Errorf("engine acquires=%d releases=%d, want 1/1", engineProvider.acquires, engineProvider.releases)
	}
	// One model call through the sealed payload seam.
	if len(modelClient.payloads) != 1 {
		t.Fatalf("model called %d times, want 1", len(modelClient.payloads))
	}
}

func TestSmartSuggestionsAppendsAndDeduplicatesAgainstStandardAndBucket(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	src := &fakeSuggestionSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			{ID: "c-1", DatasetID: "ds-1", Name: "total_amount", InferredType: domain.TypeNumber},
			{ID: "c-2", DatasetID: "ds-1", Name: "customer_id", InferredType: domain.TypeString},
		},
	}
	// The model returns one question that IS a deterministic candidate ("What is the total total
	// amount?" — generated from total_amount), one that is already in the bucket, and one genuinely
	// fresh question (all reference known safe columns, so ParseModelResponse accepts them). Only
	// the fresh question may be stored.
	modelClient := &fakeSmartModel{text: `["What is the total total amount?", "Which customer id has the highest total amount?", "What is the average total amount by customer id?"]`}
	bucket := &fakeSuggestionBucket{
		rows: []domain.SuggestionQuestion{
			{ID: "sq-1", WorkspaceID: "ws-1", Question: "Which customer id has the highest total amount?", CreatedAt: time.Now()},
		},
	}
	engineProvider := &fakeSmartEngine{}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), src, bucket, engineProvider, modelClient, &fakeObjectGetter{}, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	// Only the fresh question reached persistence.
	if len(bucket.addCalls) != 1 || len(bucket.addCalls[0]) != 1 || bucket.addCalls[0][0] != "What is the average total amount by customer id?" {
		t.Errorf("bucket.Add calls = %v, want exactly the one fresh question", bucket.addCalls)
	}
	if len(bucket.rows) != 2 {
		t.Errorf("bucket rows = %d, want 2 (pre-seeded + fresh)", len(bucket.rows))
	}
}

func TestListSuggestionsIncludesPersistedSmartBucket(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	src := &fakeSuggestionSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			{ID: "c-1", DatasetID: "ds-1", Name: "total_amount", InferredType: domain.TypeNumber},
			{ID: "c-2", DatasetID: "ds-1", Name: "customer_id", InferredType: domain.TypeString},
		},
	}
	bucket := &fakeSuggestionBucket{
		rows: []domain.SuggestionQuestion{
			{ID: "sq-1", WorkspaceID: "ws-1", Question: "Which customer id has the highest total amount?", CreatedAt: time.Now()},
		},
	}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), src, bucket, nil, nil, &fakeObjectGetter{}, newFakeUserTierLookup())

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/suggestions", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Suggestions []string                   `json:"suggestions"`
		Smart       []publicSuggestionQuestion `json:"smart"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Smart) != 1 || body.Smart[0].ID != "sq-1" || body.Smart[0].Question != "Which customer id has the highest total amount?" {
		t.Errorf("smart = %+v, want the persisted bucket row with its id", body.Smart)
	}
	if len(body.Suggestions) < 5 {
		t.Errorf("deterministic suggestions = %v, want the standard strip present", body.Suggestions)
	}
}

func TestDeleteSuggestionRemovesFromBucket(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	bucket := &fakeSuggestionBucket{
		rows: []domain.SuggestionQuestion{
			{ID: "sq-1", WorkspaceID: "ws-1", Question: "One?", CreatedAt: time.Now()},
			{ID: "sq-2", WorkspaceID: "ws-1", Question: "Two?", CreatedAt: time.Now()},
		},
	}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{}, bucket, nil, nil, nil, users)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/suggestions/sq-1", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if len(bucket.rows) != 1 || bucket.rows[0].ID != "sq-2" {
		t.Errorf("bucket after delete = %+v, want only sq-2", bucket.rows)
	}
}

func TestDeleteSuggestionReturnsNotFoundForMissingOrForeignRow(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	bucket := &fakeSuggestionBucket{
		rows: []domain.SuggestionQuestion{
			{ID: "sq-1", WorkspaceID: "ws-1", Question: "One?", CreatedAt: time.Now()},
			{ID: "sq-2", WorkspaceID: "ws-2", Question: "Someone else's?", CreatedAt: time.Now()},
		},
	}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{}, bucket, nil, nil, nil, users)

	// A row that exists but belongs to a different workspace reads as not-found, never a probe.
	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/suggestions/sq-2", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}

	// A row that never existed also reads as not-found.
	req = authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/suggestions/nope", nil, "user-1", domain.TierPro)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSuggestionRequiresMutationAccess(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	members := newFakeMemberLookupStore()
	// FEAT-045: a shared read-only member may view suggestions but not delete them.
	members.byUser["user-2"] = domain.WorkspaceMember{ID: "m-1", WorkspaceID: "ws-1", UserID: "user-2", Role: domain.RoleReadOnly}
	users := newFakeUserTierLookup()
	users.tiers["user-2"] = domain.TierPro
	bucket := &fakeSuggestionBucket{
		rows: []domain.SuggestionQuestion{
			{ID: "sq-1", WorkspaceID: "ws-1", Question: "One?", CreatedAt: time.Now()},
		},
	}
	router := newTestSuggestionsRouterWithSmart(workspaces, members, &fakeSuggestionSource{}, bucket, nil, nil, nil, users)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/suggestions/sq-1", nil, "user-2", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a read-only member; body: %s", rec.Code, rec.Body.String())
	}
	if len(bucket.rows) != 1 {
		t.Errorf("bucket mutated by a read-only member: %+v", bucket.rows)
	}
}

func TestSmartSuggestionsSurfacesEngineUnavailable(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierTeam
	engineProvider := &fakeSmartEngine{err: engine.ErrEngineUnavailable}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{}, &fakeSuggestionBucket{}, engineProvider, &fakeSmartModel{}, &fakeObjectGetter{}, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestSmartSuggestionsSurfacesModelFailure(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	modelClient := &fakeSmartModel{err: errors.New("anthropic down")}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "x.csv", IngestStatus: domain.IngestComplete}},
		columns:  []domain.Column{{ID: "c-1", DatasetID: "ds-1", Name: "amount", InferredType: domain.TypeNumber}},
	}, &fakeSuggestionBucket{}, &fakeSmartEngine{}, modelClient, &fakeObjectGetter{}, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", rec.Code, rec.Body.String())
	}
}
