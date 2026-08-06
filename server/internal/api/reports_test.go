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
