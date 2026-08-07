package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeReportRepo is an in-memory ReportStore for the report read endpoint.
type fakeReportRepo struct {
	mu      sync.Mutex
	reports map[string]domain.Report
	err     error
}

func newFakeReportRepo() *fakeReportRepo {
	return &fakeReportRepo{reports: make(map[string]domain.Report)}
}

func (f *fakeReportRepo) GetByID(_ context.Context, workspaceID, id string) (domain.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.Report{}, f.err
	}
	report, ok := f.reports[id]
	if !ok || report.WorkspaceID != workspaceID {
		// Same stance as the real store: a report that exists only in another workspace reads as
		// not found (non-enumeration), so the handler need not special-case it.
		return domain.Report{}, errors.New("report not found")
	}
	return report, nil
}

func (f *fakeReportRepo) GetByRunID(_ context.Context, workspaceID, runID string) (domain.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.Report{}, f.err
	}
	for _, report := range f.reports {
		if report.RunID == runID && report.WorkspaceID == workspaceID {
			return report, nil
		}
	}
	return domain.Report{}, errors.New("report not found")
}

// ListByWorkspace mirrors the real store's ordering (created_at DESC) and cap, and its
// workspace-scoping (a report from another workspace never appears in the list).
func (f *fakeReportRepo) ListByWorkspace(_ context.Context, workspaceID string, limit int) ([]domain.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := []domain.Report{}
	for _, report := range f.reports {
		if report.WorkspaceID != workspaceID {
			continue
		}
		out = append(out, report)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// newReportsTestServer wires the report endpoint's fakes plus the workspace store the ownership
// middleware needs, mirroring newRunsTestServer.
func newReportsTestServer(t *testing.T) (*Server, *fakeWorkspaceStore, *fakeReportRepo) {
	t.Helper()

	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: config.MaxUploadBytesDefault,
		Environment:    "development",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	workspaces := newFakeWorkspaceStore()
	reports := newFakeReportRepo()

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret: testAuthSecret,
		Workspaces: workspaces,
		Users:      newFakeUserTierLookup(),
		Reports:    reports,
	})
	return server, workspaces, reports
}

func TestGetReportRequiresAuthentication(t *testing.T) {
	server, _, _ := newReportsTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1/reports/report-1", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestGetReportSucceeds serves the fully self-contained report screens/09-report-view.md renders:
// narrative, chart spec, reconciliation summary, provenance links (empty this wave), and timestamp
// all ride on the Report object itself.
func TestGetReportSucceeds(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	started := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	reports.reports["report-1"] = domain.Report{
		ID:          "report-1",
		WorkspaceID: "ws-1",
		RunID:       "run-1",
		Title:       "Quarterly sales",
		Narrative:   "Sales rose in Q2.",
		ChartSpec: domain.ChartSpec{
			ChartType:   domain.ChartBar,
			XAxisColumn: "region",
			YAxisColumn: "cnt",
		},
		ProvenanceLinks: []string{},
		ReconciliationSummary: domain.ReconciliationSummary{
			RowsIn:            1200,
			RowsOut:           8,
			ExclusionWarnings: []string{"dropped 5 rows where region was null"},
		},
		CreatedAt: started,
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Report struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Narrative string `json:"narrative"`
			ChartSpec struct {
				ChartType string `json:"chart_type"`
			} `json:"chart_spec"`
			ProvenanceLinks       []string `json:"provenance_links"`
			ReconciliationSummary struct {
				RowsIn            int64    `json:"rows_in"`
				RowsOut           int64    `json:"rows_out"`
				ExclusionWarnings []string `json:"exclusion_warnings"`
			} `json:"reconciliation_summary"`
		} `json:"report"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Report.ID != "report-1" || body.Report.Title != "Quarterly sales" {
		t.Errorf("report = %+v", body.Report)
	}
	if body.Report.Narrative != "Sales rose in Q2." {
		t.Errorf("narrative = %q, want the seeded narrative", body.Report.Narrative)
	}
	if body.Report.ChartSpec.ChartType != string(domain.ChartBar) {
		t.Errorf("chart_type = %q, want bar", body.Report.ChartSpec.ChartType)
	}
	if body.Report.ReconciliationSummary.RowsIn != 1200 || body.Report.ReconciliationSummary.RowsOut != 8 {
		t.Errorf("reconciliation = %+v", body.Report.ReconciliationSummary)
	}
	if len(body.Report.ReconciliationSummary.ExclusionWarnings) != 1 {
		t.Errorf("exclusion_warnings = %v, want the populated warning", body.Report.ReconciliationSummary.ExclusionWarnings)
	}
	// Provenance links are always an array this wave — never JSON null.
	if body.Report.ProvenanceLinks == nil {
		t.Error("provenance_links is null, want an empty array")
	}
}

// TestGetReportNormalizesNilSlices pins the wire contract that caused a production client crash:
// a stored report whose reconciliation summary carries a nil ExclusionWarnings slice (Go marshals
// nil as JSON `null`) must be emitted as `[]`, because schema.md documents the field as string[] and
// the Report View does `summary.exclusion_warnings.length` at render time.
func TestGetReportNormalizesNilSlices(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	// Deliberately leave ExclusionWarnings and ProvenanceLinks nil — exactly the shape the run
	// pipeline produces when nothing was excluded and no provenance links were materialized.
	reports.reports["report-1"] = domain.Report{
		ID:          "report-1",
		WorkspaceID: "ws-1",
		RunID:       "run-1",
		Title:       "Total transactions",
		Narrative:   "One row.",
		ReconciliationSummary: domain.ReconciliationSummary{
			RowsIn:  1000,
			RowsOut: 1,
		},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Report struct {
			ProvenanceLinks       []string `json:"provenance_links"`
			ReconciliationSummary struct {
				ExclusionWarnings []string `json:"exclusion_warnings"`
			} `json:"reconciliation_summary"`
		} `json:"report"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	if body.Report.ReconciliationSummary.ExclusionWarnings == nil {
		t.Error("exclusion_warnings is null, want an empty array (client renders .length on it)")
	}
	if body.Report.ProvenanceLinks == nil {
		t.Error("provenance_links is null, want an empty array")
	}
}

func TestGetReportReturnsNotFoundForUnknownReport(t *testing.T) {
	server, workspaces, _ := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/nope", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "not_found" {
		t.Errorf("code = %q, want not_found", body["code"])
	}
}

// TestGetReportReturnsNotFoundForCrossWorkspaceReport pins the non-enumeration stance: a report
// belonging to a different workspace reads as a plain 404, never leaking its existence.
func TestGetReportReturnsNotFoundForCrossWorkspaceReport(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-other"] = domain.Report{
		ID: "report-other", WorkspaceID: "ws-other", Title: "secret", Narrative: "secret",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-other", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
}

func TestGetReportRejectsNonOwner(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", Title: "t", Narrative: "n",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
}

func TestListReportsRequiresAuthentication(t *testing.T) {
	server, _, _ := newReportsTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1/reports", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestListReportsSucceeds pins FEAT-034's recent-reports payload: a slim report list ordered most
// recent first (screens/04's reports?limit=10), each entry carrying just enough for the Workspace
// Home card to render the title/timestamp and link to Report View.
func TestListReportsSucceeds(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	// Inserted in non-created order to prove the list orders by created_at DESC, not insertion order.
	reports.reports["id-old"] = domain.Report{
		ID: "id-old", WorkspaceID: "ws-1", RunID: "run-old", Title: "Old report",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
		CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
	reports.reports["id-new"] = domain.Report{
		ID: "id-new", WorkspaceID: "ws-1", RunID: "run-new", Title: "New report",
		Narrative:             "The newest report.", // narrative must NOT leak into the slim payload
		ChartSpec:             domain.ChartSpec{ChartType: domain.ChartBar},
		ProvenanceLinks:       []string{},
		ReconciliationSummary: domain.ReconciliationSummary{RowsIn: 100, RowsOut: 5},
		CreatedAt:             time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports?limit=10", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Capture the raw body once before decoding — json.Decoder consumes the recorder's buffer, so a
	// later rec.Body.Bytes() would read from the end.
	rawBody := append([]byte(nil), rec.Body.Bytes()...)

	var body struct {
		Reports []publicReport `json:"reports"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	if len(body.Reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2: %+v", len(body.Reports), body.Reports)
	}
	// Most recent first.
	if body.Reports[0].ID != "id-new" || body.Reports[1].ID != "id-old" {
		t.Errorf("order = %s, %s; want id-new, id-old", body.Reports[0].ID, body.Reports[1].ID)
	}
	// Slim shape: title, created_at, run_id present.
	first := body.Reports[0]
	if first.Title != "New report" || first.RunID != "run-new" {
		t.Errorf("first report = %+v", first)
	}
	if first.CreatedAt == "" {
		t.Error("created_at is empty, want a timestamp")
	}
	// narrative/chart_spec/reconciliation_summary must not ride on the slim list payload.
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		t.Fatalf("re-unmarshaling: %v", err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw["reports"], &entries); err != nil {
		t.Fatalf("re-unmarshaling reports: %v", err)
	}
	for i, entry := range entries {
		if _, ok := entry["narrative"]; ok {
			t.Errorf("reports[%d] leaked narrative; want a slim payload", i)
		}
		if _, ok := entry["chart_spec"]; ok {
			t.Errorf("reports[%d] leaked chart_spec; want a slim payload", i)
		}
	}
}

// TestListReportsRespectsLimitAndDefault pins the list's cap semantics: an explicit limit is honored
// (most-recent kept), and no explicit limit defaults to 10 (screens/04's reports?limit=10).
func TestListReportsRespectsLimitAndDefault(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	for i := 0; i < 4; i++ {
		id := "r" + string(rune('a'+i))
		reports.reports[id] = domain.Report{
			ID: id, WorkspaceID: "ws-1", Title: id,
			ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
			CreatedAt: time.Date(2026, 8, 6, 12, i, 0, 0, time.UTC),
		}
	}

	decode := func(req *http.Request) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Reports []struct {
				ID string `json:"id"`
			} `json:"reports"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		ids := make([]string, 0, len(body.Reports))
		for _, r := range body.Reports {
			ids = append(ids, r.ID)
		}
		return ids
	}

	// limit=2 caps at the two most recent, preserving most-recent-first order.
	limited := decode(authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports?limit=2", nil, "user-1", domain.TierFree))
	if len(limited) != 2 || limited[0] != "rd" || limited[1] != "rc" {
		t.Errorf("limited = %v, want [rd rc]", limited)
	}

	// No limit defaults to 10 — all four fit within it.
	all := decode(authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports", nil, "user-1", domain.TierFree))
	if len(all) != 4 {
		t.Errorf("default-limit list = %v, want all 4", all)
	}
}

// TestListReportsReturnsEmptyForNoReports pins the empty-list contract: a workspace with no reports
// (even when a report exists in another workspace) returns an empty array — never a report the
// caller does not own, never JSON null.
func TestListReportsEmptyForNoReports(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["other"] = domain.Report{
		ID: "other", WorkspaceID: "ws-other", Title: "secret", Narrative: "secret",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Reports []publicReport `json:"reports"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	// An empty array, not JSON null — the handler builds it with make([]publicReport, 0, ...), the
	// same convention as handleListWorkspaces.
	if body.Reports == nil {
		t.Error("reports is null, want an empty array")
	}
	if len(body.Reports) != 0 {
		t.Errorf("reports = %+v, want empty (workspace ids never leak across ownership)", body.Reports)
	}
}

func TestListReportsRejectsNonOwner(t *testing.T) {
	server, workspaces, reports := newReportsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", Title: "t", Narrative: "n",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
}
