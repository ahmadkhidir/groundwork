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

// fakeConfirmationDatasetStore is an in-memory ConfirmationDatasetStore.
type fakeConfirmationDatasetStore struct {
	byID map[string]domain.Dataset
}

func (f *fakeConfirmationDatasetStore) GetByID(ctx context.Context, id string) (domain.Dataset, error) {
	ds, ok := f.byID[id]
	if !ok {
		return domain.Dataset{}, postgres.ErrDatasetNotFound
	}
	return ds, nil
}

func (f *fakeConfirmationDatasetStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
	var out []domain.Dataset
	for _, ds := range f.byID {
		if ds.WorkspaceID == workspaceID {
			out = append(out, ds)
		}
	}
	return out, nil
}

// fakeConfirmationColumnStore is an in-memory ConfirmationColumnStore.
type fakeConfirmationColumnStore struct {
	byID        map[string]domain.Column
	workspaceOf map[string]string // column id -> workspace id
	byWorkspace map[string][]domain.Column
}

func newFakeConfirmationColumnStore() *fakeConfirmationColumnStore {
	return &fakeConfirmationColumnStore{
		byID:        map[string]domain.Column{},
		workspaceOf: map[string]string{},
		byWorkspace: map[string][]domain.Column{},
	}
}

func (f *fakeConfirmationColumnStore) put(workspaceID string, c domain.Column) {
	f.byID[c.ID] = c
	f.workspaceOf[c.ID] = workspaceID
	f.byWorkspace[workspaceID] = append(f.byWorkspace[workspaceID], c)
}

func (f *fakeConfirmationColumnStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Column, error) {
	return f.byWorkspace[workspaceID], nil
}

func (f *fakeConfirmationColumnStore) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Column, error) {
	c, ok := f.byID[id]
	if !ok || f.workspaceOf[id] != workspaceID {
		return domain.Column{}, postgres.ErrColumnNotFound
	}
	return c, nil
}

func (f *fakeConfirmationColumnStore) UpdateInferredType(ctx context.Context, id string, t domain.InferredType) error {
	c := f.byID[id]
	c.InferredType = t
	// Mirrors ColumnRepository.UpdateInferredType's COALESCE (columns.go): move confidence to the
	// newly selected type's measured fit rate, keeping the existing confidence when the map lacks
	// that type (a column profiled before migration 00006 — "unknown", never fabricated 1.0).
	if rate, ok := c.TypeFitRates[t]; ok {
		c.TypeConfidence = rate
	}
	f.byID[id] = c
	return nil
}

func (f *fakeConfirmationColumnStore) UpdatePII(ctx context.Context, id string, flag bool, pattern string, reviewedAt time.Time) error {
	c := f.byID[id]
	c.PIIFlag = flag
	c.PIIPattern = pattern
	c.PIIReviewedAt = reviewedAt.Format(time.RFC3339)
	f.byID[id] = c
	return nil
}

// fakeConfirmationAuditWriter records FEAT-007's pii_override entries so a test can assert the
// override and its audit entry happen together, in the one handler.
type fakeConfirmationAuditWriter struct {
	entries []struct{ workspaceID, userID string }
	err     error
}

func (f *fakeConfirmationAuditWriter) WritePIIOverride(ctx context.Context, workspaceID, userID string) error {
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, struct{ workspaceID, userID string }{workspaceID, userID})
	return nil
}

// fakeConfirmationCandidateJoinStore is an in-memory ConfirmationCandidateJoinStore.
type fakeConfirmationCandidateJoinStore struct {
	byID map[string]domain.CandidateJoin
}

func (f *fakeConfirmationCandidateJoinStore) GetByID(ctx context.Context, id string) (domain.CandidateJoin, error) {
	cj, ok := f.byID[id]
	if !ok {
		return domain.CandidateJoin{}, postgres.ErrCandidateJoinNotFound
	}
	return cj, nil
}

func (f *fakeConfirmationCandidateJoinStore) UpdateStatus(ctx context.Context, id string, status domain.CandidateJoinStatus) error {
	cj := f.byID[id]
	cj.Status = status
	f.byID[id] = cj
	return nil
}

// fakeRelationshipStore is an in-memory ConfirmationRelationshipStore.
type fakeRelationshipStore struct {
	created []domain.Relationship
}

func (f *fakeRelationshipStore) Create(ctx context.Context, rel domain.Relationship) error {
	f.created = append(f.created, rel)
	return nil
}

func newTestConfirmationRouter(
	workspaces WorkspaceStore,
	datasets ConfirmationDatasetStore,
	columns ConfirmationColumnStore,
	joins ConfirmationCandidateJoinStore,
	relationshipStore ConfirmationRelationshipStore,
) http.Handler {
	return newTestConfirmationRouterWithAudit(workspaces, datasets, columns, joins, relationshipStore, &fakeConfirmationAuditWriter{})
}

func newTestConfirmationRouterWithAudit(
	workspaces WorkspaceStore,
	datasets ConfirmationDatasetStore,
	columns ConfirmationColumnStore,
	joins ConfirmationCandidateJoinStore,
	relationshipStore ConfirmationRelationshipStore,
	auditWriter ConfirmationAuditWriter,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterConfirmationRoutes(r, workspaces, datasets, columns, joins, relationshipStore, auditWriter,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

func TestListWorkspaceColumnsIncludesDatasetFilename(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{
		"ds-1": {ID: "ds-1", WorkspaceID: "ws-1", OriginalFilename: "orders.csv"},
	}}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "total", InferredType: domain.TypeNumber})

	router := newTestConfirmationRouter(workspaces, datasets, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/columns", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Columns []publicConfirmationColumn `json:"columns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Columns) != 1 || body.Columns[0].DatasetFilename != "orders.csv" {
		t.Fatalf("unexpected response: %+v", body.Columns)
	}
}

func TestUpdateColumnRequiresAtLeastOneField(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "total", InferredType: domain.TypeString})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for an empty override body", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateColumnOverridesInferredType(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{
		ID:             "col-1",
		DatasetID:      "ds-1",
		Name:           "zip",
		InferredType:   domain.TypeInteger,
		TypeConfidence: 0.6,
		TypeFitRates: map[domain.InferredType]float64{
			domain.TypeInteger: 0.6,
			domain.TypeString:  0.35,
			domain.TypeNumber:  0.0,
		},
	})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"inferred_type": "string"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := columns.byID["col-1"]
	if updated.InferredType != domain.TypeString {
		t.Errorf("inferred_type = %q, want %q", updated.InferredType, domain.TypeString)
	}
	// FEAT-006: confidence describes the newly selected type, from its MEASURED fit rate — never a
	// fabricated 1.0. This regression test exists because a flat 1.0 shipped once, showing "100% of
	// values fit this type" on a column where most values did not.
	if updated.TypeConfidence != 0.35 {
		t.Errorf("type_confidence = %v, want 0.35 (the measured fit rate for string)", updated.TypeConfidence)
	}

	// The response the client merges into the review table must carry the same honest number and the
	// fit-rate map the badge reads client-side (FEAT-006's same-interaction update).
	var body struct {
		Column publicColumn `json:"column"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Column.InferredType != "string" {
		t.Errorf("response inferred_type = %q, want %q", body.Column.InferredType, "string")
	}
	if body.Column.TypeConfidence != 0.35 {
		t.Errorf("response type_confidence = %v, want 0.35", body.Column.TypeConfidence)
	}
	if got := body.Column.TypeFitRates[domain.TypeString]; got != 0.35 {
		t.Errorf("response type_fit_rates[string] = %v, want 0.35", got)
	}
	if got := body.Column.TypeFitRates[domain.TypeInteger]; got != 0.6 {
		t.Errorf("response type_fit_rates[integer] = %v, want 0.6", got)
	}
}

// A column profiled before migration 00006 has an empty type_fit_rates; overriding its type must keep
// its existing type_confidence (the SQL's COALESCE fallback) rather than dropping to zero or being
// promoted to a fabricated 1.0.
func TestUpdateColumnOverrideKeepsConfidenceWhenFitRatesAbsent(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "zip", InferredType: domain.TypeInteger, TypeConfidence: 0.6})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"inferred_type": "string"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := columns.byID["col-1"]
	if updated.TypeConfidence != 0.6 {
		t.Errorf("type_confidence = %v, want existing 0.6 kept when type_fit_rates is empty", updated.TypeConfidence)
	}
}

func TestUpdateColumnRejectsAnUnrecognizedType(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "zip", InferredType: domain.TypeInteger})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"inferred_type": "not_a_type"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d for an unrecognized type", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateColumnOverridesPIIFlagAndClearsPatternWhenCleared(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "email", InferredType: domain.TypeString, PIIFlag: true, PIIPattern: "email"})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"pii_flag": false}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := columns.byID["col-1"]
	if updated.PIIFlag {
		t.Error("pii_flag should be cleared")
	}
	if updated.PIIPattern != "" {
		t.Errorf("pii_pattern = %q, want empty once un-flagged", updated.PIIPattern)
	}
}

func TestUpdateColumnManualPIIFlagDefaultsToManualPattern(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "notes", InferredType: domain.TypeString})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"pii_flag": true}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	updated := columns.byID["col-1"]
	if !updated.PIIFlag || updated.PIIPattern != "manual" {
		t.Errorf("got flag=%v pattern=%q, want flag=true pattern=\"manual\" for a user-initiated flag", updated.PIIFlag, updated.PIIPattern)
	}
}

func TestUpdateColumnRequiresWorkspaceOwnership(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "someone-else"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "total"})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"pii_flag": true}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a non-owner", rec.Code, http.StatusNotFound)
	}
}

func TestUpdateColumnRejectsAColumnFromADifferentWorkspace(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	// col-1 actually belongs to ws-other.
	columns.put("ws-other", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "total"})
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns, &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"pii_flag": true}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a column belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
}

func TestUpdateCandidateJoinRejectDoesNotCreateARelationship(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	joins := &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{
		"cj-1": {ID: "cj-1", WorkspaceID: "ws-1", SourceColumnID: "col-a", TargetColumnID: "col-b", Status: domain.JoinPending, CreatedAt: time.Now()},
	}}
	relationshipStore := &fakeRelationshipStore{}
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, newFakeConfirmationColumnStore(), joins, relationshipStore)

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/candidate-joins/cj-1", map[string]any{"status": "rejected"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if joins.byID["cj-1"].Status != domain.JoinRejected {
		t.Errorf("status = %q, want %q", joins.byID["cj-1"].Status, domain.JoinRejected)
	}
	if len(relationshipStore.created) != 0 {
		t.Errorf("got %d relationships created on reject, want 0", len(relationshipStore.created))
	}
}

func TestUpdateCandidateJoinAcceptCreatesARelationshipWithComputedCardinality(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	datasets := &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{
		"ds-customers": {ID: "ds-customers", WorkspaceID: "ws-1", RowCount: 3},
		"ds-orders":    {ID: "ds-orders", WorkspaceID: "ws-1", RowCount: 3},
	}}
	columns := newFakeConfirmationColumnStore()
	// source: customer_id, unique (3 distinct of 3 rows) -> "one" side.
	columns.put("ws-1", domain.Column{ID: "col-customer-id", DatasetID: "ds-customers", Name: "customer_id", DistinctCount: 3})
	// target: customer_id in orders, NOT unique (2 distinct of 3 rows) -> "many" side.
	columns.put("ws-1", domain.Column{ID: "col-order-customer-id", DatasetID: "ds-orders", Name: "customer_id", DistinctCount: 2})

	joins := &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{
		"cj-1": {ID: "cj-1", WorkspaceID: "ws-1", SourceColumnID: "col-customer-id", TargetColumnID: "col-order-customer-id", Status: domain.JoinPending, CreatedAt: time.Now()},
	}}
	relationshipStore := &fakeRelationshipStore{}
	router := newTestConfirmationRouter(workspaces, datasets, columns, joins, relationshipStore)

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/candidate-joins/cj-1", map[string]any{"status": "accepted"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if joins.byID["cj-1"].Status != domain.JoinAccepted {
		t.Errorf("status = %q, want %q", joins.byID["cj-1"].Status, domain.JoinAccepted)
	}
	if len(relationshipStore.created) != 1 {
		t.Fatalf("got %d relationships created, want 1", len(relationshipStore.created))
	}
	rel := relationshipStore.created[0]
	if rel.Cardinality != domain.OneToMany {
		t.Errorf("cardinality = %q, want %q (unique source, non-unique target)", rel.Cardinality, domain.OneToMany)
	}
	if rel.SourceColumnID != "col-customer-id" || rel.TargetColumnID != "col-order-customer-id" {
		t.Errorf("unexpected relationship column ids: %+v", rel)
	}
}

func TestUpdateCandidateJoinAcceptHonorsManualCardinalityOverride(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}
	columns := newFakeConfirmationColumnStore()
	joins := &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{
		"cj-1": {ID: "cj-1", WorkspaceID: "ws-1", SourceColumnID: "col-a", TargetColumnID: "col-b", Status: domain.JoinPending, CreatedAt: time.Now()},
	}}
	relationshipStore := &fakeRelationshipStore{}
	router := newTestConfirmationRouter(workspaces, datasets, columns, joins, relationshipStore)

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/candidate-joins/cj-1",
		map[string]any{"status": "accepted", "cardinality": "many_to_many", "grain_notes": "introduces one row per order line"},
		"user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if len(relationshipStore.created) != 1 {
		t.Fatalf("got %d relationships created, want 1", len(relationshipStore.created))
	}
	rel := relationshipStore.created[0]
	if rel.Cardinality != domain.ManyToMany {
		t.Errorf("cardinality = %q, want the manual override %q", rel.Cardinality, domain.ManyToMany)
	}
	if rel.GrainNotes != "introduces one row per order line" {
		t.Errorf("grain_notes = %q, want the manual override", rel.GrainNotes)
	}
}

func TestUpdateCandidateJoinRejectsAnInvalidStatus(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	joins := &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{
		"cj-1": {ID: "cj-1", WorkspaceID: "ws-1", Status: domain.JoinPending},
	}}
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, newFakeConfirmationColumnStore(), joins, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/candidate-joins/cj-1", map[string]any{"status": "pending"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d — a join can only be accepted or rejected via this endpoint, never reset to pending", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateCandidateJoinRejectsAJoinFromADifferentWorkspace(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	joins := &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{
		"cj-1": {ID: "cj-1", WorkspaceID: "ws-other", Status: domain.JoinPending},
	}}
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, newFakeConfirmationColumnStore(), joins, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/candidate-joins/cj-1", map[string]any{"status": "rejected"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d for a candidate join belonging to a different workspace", rec.Code, http.StatusNotFound)
	}
}

func TestUpdateCandidateJoinNotFound(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	joins := &fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}
	router := newTestConfirmationRouter(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, newFakeConfirmationColumnStore(), joins, &fakeRelationshipStore{})

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/candidate-joins/cj-missing", map[string]any{"status": "rejected"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestUpdateColumnPIIOverrideStampsReviewedAtAndWritesAuditEntry is FEAT-007's distinguishability
// criterion at the API layer: clearing a flag must leave behind both a timestamp on the column and a
// pii_override audit entry, so a reviewed-and-cleared column is never mistaken for one that was
// simply never flagged.
func TestUpdateColumnPIIOverrideStampsReviewedAtAndWritesAuditEntry(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "email", InferredType: domain.TypeString, PIIFlag: true, PIIPattern: "email"})
	auditWriter := &fakeConfirmationAuditWriter{}
	router := newTestConfirmationRouterWithAudit(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns,
		&fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{}, auditWriter)

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"pii_flag": false}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	updated := columns.byID["col-1"]
	if updated.PIIReviewedAt == "" {
		t.Error("pii_reviewed_at is empty after an explicit override — a cleared flag is now indistinguishable from one that was never set")
	}
	if _, err := time.Parse(time.RFC3339, updated.PIIReviewedAt); err != nil {
		t.Errorf("pii_reviewed_at = %q, want RFC 3339: %v", updated.PIIReviewedAt, err)
	}

	if len(auditWriter.entries) != 1 {
		t.Fatalf("got %d pii_override audit entries, want exactly 1 written in the same code path as the override", len(auditWriter.entries))
	}
	if auditWriter.entries[0].workspaceID != "ws-1" || auditWriter.entries[0].userID != "user-1" {
		t.Errorf("audit entry = %+v, want workspace ws-1 / user user-1", auditWriter.entries[0])
	}

	var body struct {
		Column struct {
			PIIReviewedAt string `json:"pii_reviewed_at"`
		} `json:"column"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Column.PIIReviewedAt == "" {
		t.Error("response body omits pii_reviewed_at, so the review step cannot show the reviewed state")
	}
}

// TestUpdateColumnTypeOverrideWritesNoPIIAuditEntry keeps the pii_override action type meaning what
// schema.md says it means. A type correction is a different decision and must not inflate the PII
// review trail.
func TestUpdateColumnTypeOverrideWritesNoPIIAuditEntry(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "amount", InferredType: domain.TypeString})
	auditWriter := &fakeConfirmationAuditWriter{}
	router := newTestConfirmationRouterWithAudit(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns,
		&fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{}, auditWriter)

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"inferred_type": "number"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if len(auditWriter.entries) != 0 {
		t.Errorf("got %d pii_override entries for a type-only correction, want 0", len(auditWriter.entries))
	}
	if columns.byID["col-1"].PIIReviewedAt != "" {
		t.Error("a type correction stamped pii_reviewed_at, claiming a PII review that never happened")
	}
}

// TestUpdateColumnPIIOverrideFailsWhenAuditWriteFails: the override and its evidence ship together
// or not at all. Reporting success while the audit write failed would leave a column whose model
// exposure changed with no record of who changed it.
func TestUpdateColumnPIIOverrideFailsWhenAuditWriteFails(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	columns := newFakeConfirmationColumnStore()
	columns.put("ws-1", domain.Column{ID: "col-1", DatasetID: "ds-1", Name: "email", InferredType: domain.TypeString, PIIFlag: true, PIIPattern: "email"})
	auditWriter := &fakeConfirmationAuditWriter{err: errors.New("audit store unavailable")}
	router := newTestConfirmationRouterWithAudit(workspaces, &fakeConfirmationDatasetStore{byID: map[string]domain.Dataset{}}, columns,
		&fakeConfirmationCandidateJoinStore{byID: map[string]domain.CandidateJoin{}}, &fakeRelationshipStore{}, auditWriter)

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1/columns/col-1", map[string]any{"pii_flag": false}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d when the audit entry could not be written", rec.Code, http.StatusInternalServerError)
	}
}
