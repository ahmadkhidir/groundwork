package api

import (
	"context"
	"encoding/json"
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

/* -------------------------------------------------------------------------------------------- */
/* Fakes                                                                                          */
/* -------------------------------------------------------------------------------------------- */

// fakeSemanticRelationshipStore is an in-memory SemanticRelationshipStore.
type fakeSemanticRelationshipStore struct {
	byID        map[string]domain.Relationship
	workspaceOf map[string]string
}

func newFakeSemanticRelationshipStore() *fakeSemanticRelationshipStore {
	return &fakeSemanticRelationshipStore{byID: map[string]domain.Relationship{}, workspaceOf: map[string]string{}}
}

func (f *fakeSemanticRelationshipStore) put(rel domain.Relationship) {
	f.byID[rel.ID] = rel
	f.workspaceOf[rel.ID] = rel.WorkspaceID
}

func (f *fakeSemanticRelationshipStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Relationship, error) {
	var out []domain.Relationship
	for _, rel := range f.byID {
		if rel.WorkspaceID == workspaceID {
			out = append(out, rel)
		}
	}
	return out, nil
}

func (f *fakeSemanticRelationshipStore) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Relationship, error) {
	rel, ok := f.byID[id]
	if !ok || f.workspaceOf[id] != workspaceID {
		return domain.Relationship{}, postgres.ErrRelationshipNotFound
	}
	return rel, nil
}

func (f *fakeSemanticRelationshipStore) Update(ctx context.Context, id string, cardinality domain.Cardinality, grainNotes string) error {
	rel := f.byID[id]
	rel.Cardinality = cardinality
	rel.GrainNotes = grainNotes
	f.byID[id] = rel
	return nil
}

// fakeSemanticMetricDefinitionStore is an in-memory SemanticMetricDefinitionStore.
type fakeSemanticMetricDefinitionStore struct {
	byID        map[string]domain.MetricDefinition
	workspaceOf map[string]string
	namesTaken  map[string]bool // "workspaceID|name" -> exists, used to simulate the UNIQUE constraint
	createErr   error
}

func newFakeSemanticMetricDefinitionStore() *fakeSemanticMetricDefinitionStore {
	return &fakeSemanticMetricDefinitionStore{
		byID:        map[string]domain.MetricDefinition{},
		workspaceOf: map[string]string{},
		namesTaken:  map[string]bool{},
	}
}

func nameKey(workspaceID, name string) string { return workspaceID + "|" + name }

func (f *fakeSemanticMetricDefinitionStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error) {
	var out []domain.MetricDefinition
	for _, m := range f.byID {
		if m.WorkspaceID == workspaceID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeSemanticMetricDefinitionStore) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.MetricDefinition, error) {
	m, ok := f.byID[id]
	if !ok || f.workspaceOf[id] != workspaceID {
		return domain.MetricDefinition{}, postgres.ErrMetricDefinitionNotFound
	}
	return m, nil
}

func (f *fakeSemanticMetricDefinitionStore) Create(ctx context.Context, m domain.MetricDefinition) error {
	if f.createErr != nil {
		return f.createErr
	}
	key := nameKey(m.WorkspaceID, m.Name)
	if f.namesTaken[key] {
		return postgres.ErrMetricDefinitionNameTaken
	}
	f.namesTaken[key] = true
	f.byID[m.ID] = m
	f.workspaceOf[m.ID] = m.WorkspaceID
	return nil
}

func (f *fakeSemanticMetricDefinitionStore) Update(ctx context.Context, id, name, sqlExpression, description string, updatedAt time.Time) error {
	m := f.byID[id]
	if name != m.Name {
		key := nameKey(m.WorkspaceID, name)
		if f.namesTaken[key] {
			return postgres.ErrMetricDefinitionNameTaken
		}
		delete(f.namesTaken, nameKey(m.WorkspaceID, m.Name))
		f.namesTaken[key] = true
	}
	m.Name = name
	m.SQLExpression = sqlExpression
	m.Description = description
	m.UpdatedAt = updatedAt
	f.byID[id] = m
	return nil
}

func (f *fakeSemanticMetricDefinitionStore) DeleteForWorkspace(ctx context.Context, id, workspaceID string) error {
	if f.workspaceOf[id] != workspaceID {
		return nil
	}
	delete(f.namesTaken, nameKey(workspaceID, f.byID[id].Name))
	delete(f.byID, id)
	delete(f.workspaceOf, id)
	return nil
}

// fakeSemanticCorrectionStore is an in-memory SemanticCorrectionStore (FEAT-019 read side plus
// FEAT-020's Create/Delete).
type fakeSemanticCorrectionStore struct {
	byWorkspace map[string][]domain.Correction
	byID        map[string]string // correction id -> workspace id, for ownership-scoped Delete
	deleted     []string
	// createErr, when set, makes Create fail — used to assert FEAT-020's capture is best-effort and
	// does not fail the run it is captured alongside.
	createErr error
}

func newFakeSemanticCorrectionStore() *fakeSemanticCorrectionStore {
	return &fakeSemanticCorrectionStore{
		byWorkspace: map[string][]domain.Correction{},
		byID:        map[string]string{},
	}
}

func (f *fakeSemanticCorrectionStore) add(c domain.Correction) {
	f.byWorkspace[c.WorkspaceID] = append(f.byWorkspace[c.WorkspaceID], c)
	f.byID[c.ID] = c.WorkspaceID
}

func (f *fakeSemanticCorrectionStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Correction, error) {
	return f.byWorkspace[workspaceID], nil
}

func (f *fakeSemanticCorrectionStore) Create(ctx context.Context, c domain.Correction) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.add(c)
	return nil
}

func (f *fakeSemanticCorrectionStore) Delete(ctx context.Context, workspaceID, id string) error {
	if f.byID[id] != workspaceID {
		return postgres.ErrCorrectionNotFound
	}
	f.deleted = append(f.deleted, id)
	delete(f.byID, id)
	ws := f.byWorkspace[workspaceID]
	for i, c := range ws {
		if c.ID == id {
			f.byWorkspace[workspaceID] = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	return nil
}

// fakeSemanticSummaryStore is an in-memory SemanticSummaryStore.
type fakeSemanticSummaryStore struct {
	relationshipCounts map[string]int
	metricCounts       map[string]int
	err                error
}

func (f *fakeSemanticSummaryStore) CountRelationships(ctx context.Context, workspaceID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.relationshipCounts[workspaceID], nil
}

func (f *fakeSemanticSummaryStore) CountMetricDefinitions(ctx context.Context, workspaceID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.metricCounts[workspaceID], nil
}

/* -------------------------------------------------------------------------------------------- */
/* Router helper                                                                                  */
/* -------------------------------------------------------------------------------------------- */

func newTestSemanticRouter(
	workspaces WorkspaceStore,
	relationships SemanticRelationshipStore,
	metrics SemanticMetricDefinitionStore,
	corrections SemanticCorrectionStore,
	summary SemanticSummaryStore,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterSemanticRoutes(r, workspaces, newFakeMemberLookupStore(), newFakeUserTierLookup(), relationships, metrics, corrections, summary,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

/* -------------------------------------------------------------------------------------------- */
/* Summary                                                                                        */
/* -------------------------------------------------------------------------------------------- */

func TestSemanticSummaryReturnsCounts(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	summary := &fakeSemanticSummaryStore{
		relationshipCounts: map[string]int{"ws-1": 12},
		metricCounts:       map[string]int{"ws-1": 4},
	}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, summary)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/semantic-layer/summary", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		RelationshipCount     int `json:"relationship_count"`
		MetricDefinitionCount int `json:"metric_definition_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.RelationshipCount != 12 || body.MetricDefinitionCount != 4 {
		t.Fatalf("got %+v, want relationship_count=12 metric_definition_count=4", body)
	}
}

func TestSemanticSummaryRequiresWorkspaceOwnership(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/semantic-layer/summary", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Relationships                                                                                  */
/* -------------------------------------------------------------------------------------------- */

func TestListSemanticRelationships(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	relationships := newFakeSemanticRelationshipStore()
	relationships.put(domain.Relationship{ID: "rel-1", WorkspaceID: "ws-1", SourceColumnID: "col-a", TargetColumnID: "col-b", Cardinality: domain.OneToMany, ConfirmedAt: time.Now()})
	relationships.put(domain.Relationship{ID: "rel-2", WorkspaceID: "ws-other", SourceColumnID: "col-c", TargetColumnID: "col-d", Cardinality: domain.OneToOne, ConfirmedAt: time.Now()})
	router := newTestSemanticRouter(workspaces, relationships, newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/semantic-layer/relationships", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Relationships []publicRelationship `json:"relationships"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Relationships) != 1 || body.Relationships[0].ID != "rel-1" {
		t.Fatalf("unexpected response: %+v — must not leak another workspace's relationship", body.Relationships)
	}
}

func TestUpdateSemanticRelationshipRequiresAtLeastOneField(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	relationships := newFakeSemanticRelationshipStore()
	relationships.put(domain.Relationship{ID: "rel-1", WorkspaceID: "ws-1", Cardinality: domain.OneToOne})
	router := newTestSemanticRouter(workspaces, relationships, newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/relationships/rel-1", map[string]any{}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for an empty edit body", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateSemanticRelationshipOverridesCardinalityAndGrainNotes(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	relationships := newFakeSemanticRelationshipStore()
	relationships.put(domain.Relationship{ID: "rel-1", WorkspaceID: "ws-1", Cardinality: domain.OneToMany, GrainNotes: "old note"})
	router := newTestSemanticRouter(workspaces, relationships, newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/relationships/rel-1",
		map[string]any{"cardinality": "many_to_many", "grain_notes": "one row per order line"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := relationships.byID["rel-1"]
	if updated.Cardinality != domain.ManyToMany || updated.GrainNotes != "one row per order line" {
		t.Fatalf("unexpected relationship after edit: %+v", updated)
	}
}

func TestUpdateSemanticRelationshipPartialEditPreservesUntouchedField(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	relationships := newFakeSemanticRelationshipStore()
	relationships.put(domain.Relationship{ID: "rel-1", WorkspaceID: "ws-1", Cardinality: domain.OneToMany, GrainNotes: "keep me"})
	router := newTestSemanticRouter(workspaces, relationships, newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/relationships/rel-1",
		map[string]any{"cardinality": "one_to_one"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := relationships.byID["rel-1"]
	if updated.Cardinality != domain.OneToOne {
		t.Errorf("cardinality = %q, want one_to_one", updated.Cardinality)
	}
	if updated.GrainNotes != "keep me" {
		t.Errorf("grain_notes = %q, editing cardinality alone must not touch it", updated.GrainNotes)
	}
}

func TestUpdateSemanticRelationshipRejectsUnrecognizedCardinality(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	relationships := newFakeSemanticRelationshipStore()
	relationships.put(domain.Relationship{ID: "rel-1", WorkspaceID: "ws-1", Cardinality: domain.OneToMany})
	router := newTestSemanticRouter(workspaces, relationships, newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/relationships/rel-1",
		map[string]any{"cardinality": "sideways"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for an unrecognized cardinality", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateSemanticRelationshipNotFoundAcrossWorkspaces(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	relationships := newFakeSemanticRelationshipStore()
	relationships.put(domain.Relationship{ID: "rel-1", WorkspaceID: "ws-other", Cardinality: domain.OneToMany})
	router := newTestSemanticRouter(workspaces, relationships, newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/relationships/rel-1",
		map[string]any{"grain_notes": "x"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a relationship belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Metric definitions                                                                             */
/* -------------------------------------------------------------------------------------------- */

func TestCreateMetricDefinition(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	metrics := newFakeSemanticMetricDefinitionStore()
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), metrics, &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/semantic-layer/metrics",
		map[string]any{"name": "revenue", "sql_expression": "SUM(total)", "description": "Gross revenue."}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		MetricDefinition publicMetricDefinition `json:"metric_definition"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.MetricDefinition.Name != "revenue" || body.MetricDefinition.SQLExpression != "SUM(total)" {
		t.Fatalf("unexpected response: %+v", body.MetricDefinition)
	}
	if body.MetricDefinition.WorkspaceID != "ws-1" {
		t.Errorf("workspace_id = %q, want ws-1", body.MetricDefinition.WorkspaceID)
	}
}

func TestCreateMetricDefinitionRejectsBlankNameOrExpression(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/semantic-layer/metrics",
		map[string]any{"name": "  ", "sql_expression": "SUM(total)"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for a blank name", rec.Code, http.StatusBadRequest)
	}
}

func TestCreateMetricDefinitionRejectsDuplicateName(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	metrics := newFakeSemanticMetricDefinitionStore()
	metrics.namesTaken[nameKey("ws-1", "revenue")] = true
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), metrics, &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/semantic-layer/metrics",
		map[string]any{"name": "revenue", "sql_expression": "SUM(total)"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d for a duplicate metric name in the same workspace", rec.Code, http.StatusConflict)
	}
}

func TestUpdateMetricDefinitionPartialEditPreservesUntouchedFields(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	metrics := newFakeSemanticMetricDefinitionStore()
	now := time.Now()
	metrics.byID["metric-1"] = domain.MetricDefinition{ID: "metric-1", WorkspaceID: "ws-1", Name: "revenue", SQLExpression: "SUM(total)", Description: "old desc", CreatedAt: now, UpdatedAt: now}
	metrics.workspaceOf["metric-1"] = "ws-1"
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), metrics, &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/metrics/metric-1",
		map[string]any{"description": "new desc"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := metrics.byID["metric-1"]
	if updated.Description != "new desc" {
		t.Errorf("description = %q, want %q", updated.Description, "new desc")
	}
	if updated.Name != "revenue" || updated.SQLExpression != "SUM(total)" {
		t.Errorf("editing description alone changed other fields: %+v", updated)
	}
}

func TestUpdateMetricDefinitionRequiresAtLeastOneField(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	metrics := newFakeSemanticMetricDefinitionStore()
	metrics.byID["metric-1"] = domain.MetricDefinition{ID: "metric-1", WorkspaceID: "ws-1", Name: "revenue", SQLExpression: "SUM(total)"}
	metrics.workspaceOf["metric-1"] = "ws-1"
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), metrics, &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/metrics/metric-1", map[string]any{}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for an empty edit body", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateMetricDefinitionNotFound(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/semantic-layer/metrics/missing",
		map[string]any{"description": "x"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDeleteMetricDefinition(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	metrics := newFakeSemanticMetricDefinitionStore()
	metrics.byID["metric-1"] = domain.MetricDefinition{ID: "metric-1", WorkspaceID: "ws-1", Name: "revenue"}
	metrics.workspaceOf["metric-1"] = "ws-1"
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), metrics, &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/semantic-layer/metrics/metric-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := metrics.byID["metric-1"]; ok {
		t.Error("metric definition still present after delete")
	}
}

func TestDeleteMetricDefinitionFromAnotherWorkspaceNotFound(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	metrics := newFakeSemanticMetricDefinitionStore()
	metrics.byID["metric-1"] = domain.MetricDefinition{ID: "metric-1", WorkspaceID: "ws-other", Name: "revenue"}
	metrics.workspaceOf["metric-1"] = "ws-other"
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), metrics, &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/semantic-layer/metrics/metric-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a metric belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
	if _, ok := metrics.byID["metric-1"]; !ok {
		t.Error("metric definition belonging to another workspace was deleted")
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Corrections                                                                                    */
/* -------------------------------------------------------------------------------------------- */

func TestListCorrectionsEmptyWorkspaceReturnsEmptyList(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/semantic-layer/corrections", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Corrections []publicCorrection `json:"corrections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Corrections) != 0 {
		t.Fatalf("got %d corrections for an unbuilt-FEAT-020 workspace, want 0 (legitimate Empty state)", len(body.Corrections))
	}
}

func TestListCorrectionsDisablesRunLinkWhenOriginatingRunIsGone(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	corrections := &fakeSemanticCorrectionStore{byWorkspace: map[string][]domain.Correction{
		"ws-1": {
			{ID: "corr-1", WorkspaceID: "ws-1", Description: "d", OriginalLogic: "o", CorrectedLogic: "c", OriginatingRunID: "", CreatedAt: time.Now()},
		},
	}}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), corrections, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/semantic-layer/corrections", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Corrections []publicCorrection `json:"corrections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Corrections) != 1 {
		t.Fatalf("got %d corrections, want 1", len(body.Corrections))
	}
	if body.Corrections[0].OriginatingRunID != "" {
		t.Errorf("originating_run_id = %q, want empty so the client disables the run link", body.Corrections[0].OriginatingRunID)
	}
}

// TestDeleteCorrection pins FEAT-020 AC3's happy path: the remove affordance deletes the correction
// and answers 204.
func TestDeleteCorrection(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	corrections := newFakeSemanticCorrectionStore()
	corrections.add(domain.Correction{ID: "corr-1", WorkspaceID: "ws-1", Description: "d", CorrectedLogic: "c", CreatedAt: time.Now()})
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), corrections, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/semantic-layer/corrections/corr-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if len(corrections.byWorkspace["ws-1"]) != 0 {
		t.Error("correction still listed after delete")
	}
	if len(corrections.deleted) != 1 || corrections.deleted[0] != "corr-1" {
		t.Errorf("deleted = %v, want [corr-1]", corrections.deleted)
	}
}

// TestDeleteCorrectionFromAnotherWorkspaceNotFound pins FEAT-020's workspace-ownership enforcement:
// a correction in workspace2 must read as a plain 404 from workspace1 (non-enumeration) and must not
// actually be removed.
func TestDeleteCorrectionFromAnotherWorkspaceNotFound(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	corrections := newFakeSemanticCorrectionStore()
	corrections.add(domain.Correction{ID: "corr-other", WorkspaceID: "ws-other", Description: "d", CorrectedLogic: "c", CreatedAt: time.Now()})
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), corrections, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/semantic-layer/corrections/corr-other", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a correction belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
	if len(corrections.deleted) != 0 {
		t.Error("correction belonging to another workspace was deleted")
	}
	if len(corrections.byWorkspace["ws-other"]) != 1 {
		t.Error("cross-workspace correction was removed from its own workspace's list")
	}
}

// TestDeleteCorrectionNotFound pins the 404 for a correction id that never existed.
func TestDeleteCorrectionNotFound(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), newFakeSemanticCorrectionStore(), &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/semantic-layer/corrections/missing", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestDeleteCorrectionRejectsNonOwner pins that a delete request for a workspace the caller does not
// own is blocked by workspace ownership before any store call.
func TestDeleteCorrectionRejectsNonOwner(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}
	corrections := newFakeSemanticCorrectionStore()
	corrections.add(domain.Correction{ID: "corr-1", WorkspaceID: "ws-1", CreatedAt: time.Now()})
	router := newTestSemanticRouter(workspaces, newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), corrections, &fakeSemanticSummaryStore{})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/semantic-layer/corrections/corr-1", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
	if len(corrections.deleted) != 0 {
		t.Error("non-owner's delete reached the store")
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Cross-cutting: workspace ownership on every route                                              */
/* -------------------------------------------------------------------------------------------- */

func TestSemanticRoutesRequireAuthentication(t *testing.T) {
	router := newTestSemanticRouter(newFakeWorkspaceStore(), newFakeSemanticRelationshipStore(), newFakeSemanticMetricDefinitionStore(), &fakeSemanticCorrectionStore{}, &fakeSemanticSummaryStore{})

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1/semantic-layer/relationships", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d for an unauthenticated request", rec.Code, http.StatusUnauthorized)
	}
}
