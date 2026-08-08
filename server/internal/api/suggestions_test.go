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
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
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
// engine/model nil by default — the smart route is registered only when both are wired.
func newTestSuggestionsRouter(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource) http.Handler {
	return newTestSuggestionsRouterWithSmart(workspaces, members, source, nil, nil, newFakeUserTierLookup())
}

func newTestSuggestionsRouterWithSmart(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource, engineProvider SuggestionEngineProvider, modelClient SmartSuggestionModel, users UserTierLookup) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			RegisterSuggestionRoutes(r, workspaces, members, users, source, engineProvider, modelClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
		})
	})
	return r
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

func TestSmartSuggestionsRejectsFreeTierBeforeAnyWork(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup() // defaults to Free
	engineProvider := &fakeSmartEngine{}
	modelClient := &fakeSmartModel{text: `["What is the total amount?"]`}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{}, engineProvider, modelClient, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	if engineProvider.acquires != 0 {
		t.Errorf("engine acquired %d times on a Free-tier request, want 0", engineProvider.acquires)
	}
	if len(modelClient.payloads) != 0 {
		t.Error("model called on a Free-tier request, want 0 calls")
	}
}

func TestSmartSuggestionsGeneratesForProUser(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	engineProvider := &fakeSmartEngine{}
	modelClient := &fakeSmartModel{text: `["What is the total total amount?", "What are the most common customer id values?"]`}
	src := &fakeSuggestionSource{
		datasets: []domain.Dataset{{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}},
		columns: []domain.Column{
			{ID: "c-1", DatasetID: "ds-1", Name: "total_amount", InferredType: domain.TypeNumber},
			{ID: "c-2", DatasetID: "ds-1", Name: "customer_id", InferredType: domain.TypeString},
		},
	}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), src, engineProvider, modelClient, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Suggestions    []string `json:"suggestions"`
		ModelGenerated bool     `json:"model_generated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if !body.ModelGenerated || len(body.Suggestions) != 2 {
		t.Errorf("body = %+v", body)
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

func TestSmartSuggestionsSurfacesEngineUnavailable(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierTeam
	engineProvider := &fakeSmartEngine{err: engine.ErrEngineUnavailable}
	router := newTestSuggestionsRouterWithSmart(workspaces, newFakeMemberLookupStore(), &fakeSuggestionSource{}, engineProvider, &fakeSmartModel{}, users)

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
	}, &fakeSmartEngine{}, modelClient, users)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/suggestions/smart", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", rec.Code, rec.Body.String())
	}
}
