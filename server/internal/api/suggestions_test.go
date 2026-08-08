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

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
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
func newTestSuggestionsRouter(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			RegisterSuggestionRoutes(r, workspaces, members, source, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
