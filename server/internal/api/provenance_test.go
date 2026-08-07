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

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeProvenanceStore is an in-memory ProvenanceStore for the provenance endpoint tests.
type fakeProvenanceStore struct {
	mu     sync.Mutex
	byID   map[string]domain.ProvenanceLink
	report string
	links  []domain.ProvenanceLink
	err    error
}

func newFakeProvenanceStore() *fakeProvenanceStore {
	return &fakeProvenanceStore{byID: make(map[string]domain.ProvenanceLink)}
}

func (f *fakeProvenanceStore) ListByReport(_ context.Context, reportID string) ([]domain.ProvenanceLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if reportID != f.report {
		return []domain.ProvenanceLink{}, nil
	}
	return f.links, nil
}

func (f *fakeProvenanceStore) GetByID(_ context.Context, workspaceID, id string) (domain.ProvenanceLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	link, ok := f.byID[id]
	if !ok {
		return domain.ProvenanceLink{}, errors.New("claim not found")
	}
	return link, nil
}

// newProvenanceTestServer wires the report, provenance, and run-step fakes plus the workspace and
// user stores the middleware needs, mirroring newReportsTestServer.
func newProvenanceTestServer(t *testing.T) (*Server, *fakeWorkspaceStore, *fakeReportRepo, *fakeProvenanceStore, *fakeRunStepRepo) {
	workspaces := newFakeWorkspaceStore()
	reports := newFakeReportRepo()
	prov := newFakeProvenanceStore()
	steps := newFakeRunStepRepo()

	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: config.MaxUploadBytesDefault,
		Environment:    "development",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret: testAuthSecret,
		Workspaces: workspaces,
		Users:      newFakeUserTierLookup(),
		Reports:    reports,
		Provenance: prov,
		RunSteps:   steps,
	})
	return server, workspaces, reports, prov, steps
}

func TestProvenanceClaimDetailServesStatedFigureAndSource(t *testing.T) {
	server, workspaces, reports, prov, steps := newProvenanceTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	report := makeProvenanceReport("report-1", "run-1")
	reports.reports["report-1"] = report

	prov.report = "report-1"
	prov.links = []domain.ProvenanceLink{makeProvenanceLink("claim-1", "report-1")}
	prov.byID["claim-1"] = makeProvenanceLink("claim-1", "report-1")

	// The run's persisted execute step: the source the panel reconstructs the claimed cell from.
	steps.steps["run-1"] = []domain.Step{{
		ID:       "step-exec",
		RunID:    "run-1",
		StepName: domain.StepExecute,
		Output:   marshalExecuteJSON([]string{"region", "cnt"}, [][]any{{"North", float64(214)}}),
	}}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/provenance/claim-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Detail struct {
			Claim struct {
				ClaimText   string   `json:"claim_text"`
				StatedValue string   `json:"stated_value"`
				RunID       string   `json:"run_id"`
				SourceCol   string   `json:"source_column"`
				SourceRows  []string `json:"source_row_ids"`
			} `json:"claim"`
			Source struct {
				Available       bool       `json:"available"`
				Columns         []string   `json:"columns"`
				Rows            [][]string `json:"rows"`
				HighlightColumn string     `json:"highlight_column"`
			} `json:"source"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Detail.Claim.StatedValue != "214" {
		t.Errorf("StatedValue = %q, want 214", body.Detail.Claim.StatedValue)
	}
	if body.Detail.Claim.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1 (for View full run)", body.Detail.Claim.RunID)
	}
	if !body.Detail.Source.Available {
		t.Fatalf("source available = false, want true")
	}
	if len(body.Detail.Source.Rows) != 1 || body.Detail.Source.Rows[0][0] != "North" || body.Detail.Source.Rows[0][1] != "214" {
		t.Errorf("source rows = %+v, want the claimed cell's row", body.Detail.Source.Rows)
	}
	if body.Detail.Source.HighlightColumn != "cnt" {
		t.Errorf("highlight_column = %q, want cnt", body.Detail.Source.HighlightColumn)
	}
}

func TestProvenanceClaimSourceUnavailableWhenExecuteOutputGone(t *testing.T) {
	server, workspaces, reports, prov, _ := newProvenanceTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = makeProvenanceReport("report-1", "run-1")
	prov.byID["claim-1"] = makeProvenanceLink("claim-1", "report-1")

	// No run steps: the claimed source is honestly unavailable, not an error.
	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/provenance/claim-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (unavailable is a state, not an error)", rec.Code, http.StatusOK)
	}
	var body struct {
		Detail struct {
			Source struct {
				Available bool `json:"available"`
			} `json:"source"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Detail.Source.Available {
		t.Error("source available = true, want false when execute output is gone")
	}
}

func TestProvenanceClaimNotFoundForForeignReport(t *testing.T) {
	server, workspaces, reports, prov, _ := newProvenanceTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = makeProvenanceReport("report-1", "run-1")
	// claim belongs to a different report -> non-enumeration 404.
	prov.byID["claim-1"] = makeProvenanceLink("claim-1", "report-OTHER")

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/provenance/claim-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// -- helpers --

func makeProvenanceReport(id, runID string) domain.Report {
	return domain.Report{
		ID:          id,
		WorkspaceID: "ws-1",
		RunID:       runID,
		Title:       "Quarterly sales",
		Narrative:   "Sales rose in Q2.",
		CreatedAt:   time.Now().UTC(),
	}
}

func makeProvenanceLink(id, reportID string) domain.ProvenanceLink {
	return domain.ProvenanceLink{
		ID:           id,
		ReportID:     reportID,
		ClaimText:    "214 customers in North",
		StatedValue:  "214",
		SourceQuery:  "SELECT region, cnt FROM revenue",
		SourceColumn: "cnt",
		SourceRowIDs: []string{"0"},
	}
}

// marshalExecuteJSON renders an ExecuteStepOutput as the persisted step output JSON the provenance
// handler reconstructs source rows from.
func marshalExecuteJSON(cols []string, rows [][]any) string {
	b, _ := json.Marshal(domain.ExecuteStepOutput{Columns: cols, Rows: rows, RowCount: len(rows)})
	return string(b)
}
