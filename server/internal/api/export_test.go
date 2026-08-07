package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeExportAuditWriter records WriteExport calls so a test can assert the export and its audit
// entry happen together, in the one handler (the same fake shape as fakeConfirmationAuditWriter).
type fakeExportAuditWriter struct {
	mu      sync.Mutex
	entries []struct{ workspaceID, userID string }
	err     error
}

func (f *fakeExportAuditWriter) WriteExport(ctx context.Context, workspaceID, userID string) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, struct{ workspaceID, userID string }{workspaceID, userID})
	return nil
}

func (f *fakeExportAuditWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

// newExportTestRouter wires RegisterExportRoutes onto a fresh chi router behind the auth
// middleware, mirroring newTestConfirmationRouterWithAudit — the handler is exercised through its
// real route without requiring the Dependencies wiring change (which the orchestrator applies in
// api.go during assembly). provenance may be nil: the export still works, just without claim
// expansion in the JSON export (same optionality as RegisterReportRoutes).
func newExportTestRouter(
	workspaces WorkspaceStore,
	reports ReportStore,
	steps RunStepStore,
	users UserTierLookup,
	audit ExportAuditWriter,
	prov ProvenanceStore,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterExportRoutes(r, workspaces, newFakeMemberLookupStore(), reports, steps, users, audit, prov, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

// newExportTestServer returns the router plus its fakes, so each test can seed state and assert
// against the same in-memory stores.
func newExportTestServer(t *testing.T) (http.Handler, *fakeWorkspaceStore, *fakeReportRepo, *fakeRunStepRepo, *fakeUserTierLookup, *fakeExportAuditWriter, *fakeProvenanceStore) {
	t.Helper()
	workspaces := newFakeWorkspaceStore()
	reports := newFakeReportRepo()
	steps := newFakeRunStepRepo()
	users := newFakeUserTierLookup()
	audit := &fakeExportAuditWriter{}
	prov := newFakeProvenanceStore()
	return newExportTestRouter(workspaces, reports, steps, users, audit, prov), workspaces, reports, steps, users, audit, prov
}

// seedExportFixture wires a workspace + report + persisted execute step into the fakes, the
// minimal state every happy-path export test needs.
func seedExportFixture(workspaces *fakeWorkspaceStore, reports *fakeReportRepo, steps *fakeRunStepRepo) {
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID:          "report-1",
		WorkspaceID: "ws-1",
		RunID:       "run-1",
		Title:       "Customers by region",
		Narrative:   "North leads the count with 10 customers.",
		ChartSpec: domain.ChartSpec{
			ChartType:   domain.ChartBar,
			XAxisColumn: "region",
			YAxisColumn: "cnt",
			Title:       "Customers by region",
		},
		ProvenanceLinks: []string{},
		ReconciliationSummary: domain.ReconciliationSummary{
			RowsIn:            100,
			RowsOut:           2,
			ExclusionWarnings: []string{"excluded 31% of rows where region was null"},
		},
		CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}
	steps.steps["run-1"] = []domain.Step{
		{
			ID:             "step-execute",
			RunID:          "run-1",
			StepName:       domain.StepExecute,
			Status:         domain.StepComplete,
			IdempotencyKey: "run-1:execute",
			Output:         `{"columns":["region","cnt"],"rows":[["North",10],["South",8]],"row_count":2}`,
		},
	}
}

// seedExportProvenance wires one expanded claim into the fixture report + provenance fake, so the
// JSON/PPTX/Markdown/HTML exports have real claim objects to embed (FEAT-039/FEAT-037/FEAT-038).
func seedExportProvenance(reports *fakeReportRepo, prov *fakeProvenanceStore) {
	report := reports.reports["report-1"]
	report.ProvenanceLinks = []string{"claim-1"}
	reports.reports["report-1"] = report
	prov.report = "report-1"
	prov.links = []domain.ProvenanceLink{
		{
			ID:           "claim-1",
			ReportID:     "report-1",
			ClaimText:    "North leads the count with 10 customers.",
			StatedValue:  "10",
			SourceQuery:  "SELECT region, count(*) AS cnt FROM data GROUP BY region ORDER BY cnt DESC",
			SourceColumn: "cnt",
			SourceRowIDs: []string{"0"},
		},
	}
}

// TestExportRequiresAuthentication pins that the export endpoint is behind the auth boundary like
// every other report endpoint.
func TestExportRequiresAuthentication(t *testing.T) {
	handler, _, _, _, _, _, _ := newExportTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pdf", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestExportRejectsNonOwner pins the ownership boundary: a non-owner gets a plain 404
// (non-enumeration), never the bytes.
func TestExportRejectsNonOwner(t *testing.T) {
	handler, workspaces, _, _, _, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pdf", nil, "someone-else", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if audit.count() != 0 {
		t.Error("a denied export must not write an audit entry")
	}
}

// TestExportInvalidFormat rejects an unregistered format with a validation error before any data
// work happens. Ownership is verified first (the same non-enumeration order as every other
// endpoint: a caller who does not own the workspace learns nothing about the endpoint's shape), so
// the fixture seeds the workspace. "docx" is deliberately NOT a registered ExportFormat — pptx,
// markdown, html, json, and image all are (Phase 5), so the old pptx-based negative case no longer
// applies.
func TestExportInvalidFormat(t *testing.T) {
	handler, workspaces, _, _, _, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=docx", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "invalid_format" {
		t.Errorf("code = %q, want invalid_format", body["code"])
	}
	if audit.count() != 0 {
		t.Error("an invalid-format request must not write an audit entry")
	}
}

// TestExportReportNotFound returns a plain 404 for an unknown report id (and never an audit entry).
func TestExportReportNotFound(t *testing.T) {
	handler, workspaces, _, _, _, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/nope/export?format=csv", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if audit.count() != 0 {
		t.Error("a not-found export must not write an audit entry")
	}
}

// TestExportCSVForProSucceeds is the FEAT-036 happy path: a Pro user exports the report's
// underlying result set as CSV, and the body matches the persisted execute step exactly — the same
// facts the in-app chart and data table draw from (FEAT-036 AC2).
func TestExportCSVForProSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=csv", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="customers-by-region-`) || !strings.HasSuffix(cd, `-results.csv"`) {
		t.Errorf("Content-Disposition = %q, want an attachment filename", cd)
	}
	want := "region,cnt\nNorth,10\nSouth,8\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("csv body = %q, want %q", got, want)
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
	if audit.entries[0].workspaceID != "ws-1" || audit.entries[0].userID != "user-1" {
		t.Errorf("audit entry = %+v, want ws-1/user-1", audit.entries[0])
	}
}

// TestExportCSVDeniedForFree pins monetization.md's FEAT-036 gate: a Free user gets a 403
// upgrade_required, never the data. The row is shown (never hidden) but not selectable — enforced
// server-side, since a Free user hitting the endpoint directly is exactly screens/11's edge case.
func TestExportCSVDeniedForFree(t *testing.T) {
	handler, workspaces, reports, steps, _, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=csv", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "upgrade_required" {
		t.Errorf("code = %q, want upgrade_required", body["code"])
	}
	if audit.count() != 0 {
		t.Error("a denied export must not write an audit entry")
	}
}

// TestExportCSVAllowedForTeam pins the Team tier on the same gate (FEAT-036: Pro/Team).
func TestExportCSVAllowedForTeam(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	users.tiers["user-1"] = domain.TierTeam

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=csv", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportGateUsesAuthoritativeTier pins the seam's whole point: the gate reads the tier from the
// user store (UserTierLookup), never the session cookie's tier claim — a cookie claiming Pro while
// the store says Free must be denied, and a cookie claiming Free while the store says Pro must be
// allowed.
func TestExportGateUsesAuthoritativeTier(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)

	// Cookie claims Pro; the authoritative store says Free → denied.
	users.tiers["user-1"] = domain.TierFree
	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=csv", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie-claims-pro/store-says-free: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// Cookie claims Free; the authoritative store says Pro → allowed.
	users.tiers["user-1"] = domain.TierPro
	req = authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=csv", nil, "user-1", domain.TierFree)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie-claims-free/store-says-pro: status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want exactly the one allowed export", audit.count())
	}
}

// TestExportPDFAllowedForFree pins FEAT-035's gate (PDF is Free/Pro/Team) AND the PDF artifact
// itself: a real %PDF- document with an attachment disposition, not a JSON error.
func TestExportPDFAllowedForFree(t *testing.T) {
	handler, workspaces, reports, steps, _, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pdf", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	body := rec.Body.Bytes()
	if !strings.HasPrefix(string(body), "%PDF-") {
		t.Errorf("pdf body does not start with %%PDF- (got %d bytes)", len(body))
	}
	if len(body) < 500 {
		t.Errorf("pdf body = %d bytes, want a non-trivial document", len(body))
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportPDFAllowedForPro covers the Pro tier on the same PDF path (and Team via the gate map
// already asserted in TestExportCSVAllowedForTeam; PDF shares the Free-allowed map row).
func TestExportPDFAllowedForPro(t *testing.T) {
	handler, workspaces, reports, steps, users, _, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pdf", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "customers-by-region-") || !strings.HasSuffix(cd, ".pdf\"") {
		t.Errorf("Content-Disposition = %q, want a .pdf attachment", cd)
	}
}

// TestExportCSVResultDataUnavailable pins the honest failure for CSV when the run's execute step is
// gone: a 409 naming the condition (result_data_unavailable), never a fabricated row set and never
// a 500 implying the server is at fault.
func TestExportCSVResultDataUnavailable(t *testing.T) {
	handler, workspaces, reports, _, users, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-1", Title: "t", Narrative: "n",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}
	users.tiers["user-1"] = domain.TierPro
	// No steps seeded: run-1 has no execute step at all.

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=csv", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "result_data_unavailable" {
		t.Errorf("code = %q, want result_data_unavailable", body["code"])
	}
	if audit.count() != 0 {
		t.Error("an unexportable export must not write an audit entry")
	}
}

// TestExportPDFDegradesWithoutExecuteOutput pins the PDF's honest degradation: when the execute
// output is gone, the PDF still exports (narrative-only with a note), rather than failing — the
// report's narrative is self-contained, and the printed document states the chart gap instead of
// fabricating one (mirrors the provenance panel's source-unavailable state).
func TestExportPDFDegradesWithoutExecuteOutput(t *testing.T) {
	handler, workspaces, reports, _, users, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-1", Title: "Narrative only",
		Narrative: "The numbers are in the text.", ChartSpec: domain.ChartSpec{ChartType: domain.ChartBar},
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}
	users.tiers["user-1"] = domain.TierFree

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pdf", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.HasPrefix(string(rec.Body.Bytes()), "%PDF-") {
		t.Errorf("pdf body does not start with %%PDF-")
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportAuditFailureFailsClosed pins the audit discipline: an export whose audit write fails is
// a failed request — the file is not served, because an export the log cannot account for is the
// exact silent-drift state FEAT-042 exists to prevent (the same stance as datasets.go).
func TestExportAuditFailureFailsClosed(t *testing.T) {
	handler, workspaces, reports, steps, _, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	audit.err = context.DeadlineExceeded

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pdf", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.HasPrefix(string(rec.Body.Bytes()), "%PDF-") {
		t.Error("the PDF bytes were served despite the failed audit write; want fail-closed")
	}
}

// TestBuildCSVMatchesExecuteOutput is the builder-level pin of FEAT-036 AC2: every cell type the
// execute step can carry is exported exactly as the in-app table renders it (formatCell), including
// nulls as empty, booleans, and floats.
func TestBuildCSVMatchesExecuteOutput(t *testing.T) {
	exec := domain.ExecuteStepOutput{
		Columns: []string{"name", "amount", "active", "note"},
		Rows: [][]any{
			{"Widget", 12.5, true, nil},
			{"Gadget", json.Number("42"), false, "hello, world"},
		},
		RowCount: 2,
	}

	body, err := buildCSV(exec)
	if err != nil {
		t.Fatalf("buildCSV: %v", err)
	}
	want := "name,amount,active,note\nWidget,12.5,true,\nGadget,42,false,\"hello, world\"\n"
	if got := string(body); got != want {
		t.Errorf("csv = %q, want %q", got, want)
	}
}

// TestComputeChartPoints pins the PDF chart geometry's data flattening against ChartRenderer's:
// the label is the x cell rendered as display text (null → the visible "—"), the value coerces
// numeric strings and numbers, and booleans are not numeric.
func TestComputeChartPoints(t *testing.T) {
	spec := domain.ChartSpec{ChartType: domain.ChartBar, XAxisColumn: "region", YAxisColumn: "cnt"}
	exec := domain.ExecuteStepOutput{
		Columns: []string{"region", "cnt"},
		Rows:    [][]any{{"North", 10.0}, {"South", "8"}, {"West", true}},
	}

	points := computeChartPoints(spec, exec)
	if len(points) != 3 {
		t.Fatalf("len(points) = %d, want 3", len(points))
	}
	if points[0].label != "North" || !points[0].hasValue || points[0].value != 10 {
		t.Errorf("points[0] = %+v, want North/10", points[0])
	}
	if points[1].label != "South" || !points[1].hasValue || points[1].value != 8 {
		t.Errorf("points[1] = %+v, want South/8 (numeric string coerced)", points[1])
	}
	if points[2].hasValue {
		t.Errorf("points[2] = %+v, want hasValue=false (boolean is not numeric)", points[2])
	}
}

// TestSanitizePdfText pins the WinAnsi mapping: ASCII and Latin-1 pass through, common typographic
// runes map to their cp1252 bytes, and non-encodable runes degrade to "?" rather than corrupting
// the PDF stream. Expected values use \xNN escapes because the output is a single-byte WinAnsi
// string, not UTF-8 text.
func TestSanitizePdfText(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain text 123", "plain text 123"},
		{"café — 100%", "caf\xe9 \x97 100%"}, // é (Latin-1 0xE9) kept; em dash → cp1252 0x97
		{"€5.00", "\x805.00"},                // euro → cp1252 0x80
		{"“quoted”", "\x93quoted\x94"},       // curly quotes → cp1252 0x93/0x94
		{"日本語", "???"},                       // no WinAnsi encoding → "?"
	}
	for _, tc := range cases {
		if got := sanitizePdfText(tc.in); got != tc.want {
			t.Errorf("sanitizePdfText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSlugify pins the Content-Disposition filename sanitizer: lowercase, [a-z0-9] only, dashes
// between words, and a safe fallback for titles with nothing slug-safe. Non-ASCII letters are
// dropped rather than transliterated — the slug is cosmetic (a downloads-folder filename), and a
// transliteration table would be a second source of truth for characters the PDF sanitizer already
// handles differently.
func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Customers by region", "customers-by-region"},
		{"Q3 Revenue — Final", "q3-revenue-final"},
		{"N/A", "n-a"},
		{"   ", ""},
		{"Über & Töchter", "ber-t-chter"},
	}
	for _, tc := range cases {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := exportFilename(domain.Report{ID: "1234567890abcdef", Title: "Sales"}, FormatCSV); got != "sales-12345678-results.csv" {
		t.Errorf("exportFilename(csv) = %q, want sales-12345678-results.csv", got)
	}
	if got := exportFilename(domain.Report{ID: "1234567890abcdef", Title: "Sales"}, FormatPDF); got != "sales-12345678.pdf" {
		t.Errorf("exportFilename(pdf) = %q, want sales-12345678.pdf", got)
	}
}

// TestExportFilenamesForNewFormats pins the Content-Disposition filenames for the Phase 5 formats
// (FEAT-037..040): each extension is distinct so two exports never collide in a downloads folder.
func TestExportFilenamesForNewFormats(t *testing.T) {
	cases := []struct {
		format ExportFormat
		want   string
	}{
		{FormatPPTX, "sales-12345678.pptx"},
		{FormatMarkdown, "sales-12345678.md"},
		{FormatHTML, "sales-12345678.html"},
		{FormatJSON, "sales-12345678.json"},
		{FormatImage, "sales-12345678-chart.svg"},
	}
	for _, tc := range cases {
		if got := exportFilename(domain.Report{ID: "1234567890abcdef", Title: "Sales"}, tc.format); got != tc.want {
			t.Errorf("exportFilename(%s) = %q, want %q", tc.format, got, tc.want)
		}
	}
}

// TestExportDeniedMessages pins the per-format 403 texts: each names the plan(s) that unlock it,
// per monetization.md's Feature Gate Mapping, so "always show what you're missing" reaches the API
// response as well as the dialog rows.
func TestExportDeniedMessages(t *testing.T) {
	cases := []struct {
		format ExportFormat
		want   string
	}{
		{FormatCSV, "Pro and Team"},
		{FormatPPTX, "Team plan"},
		{FormatMarkdown, "Pro and Team"},
		{FormatHTML, "Pro and Team"},
		{FormatJSON, "Pro and Team"},
		{FormatImage, "Pro and Team"},
	}
	for _, tc := range cases {
		if got := exportDeniedMessage(tc.format); !strings.Contains(got, tc.want) {
			t.Errorf("exportDeniedMessage(%s) = %q, want it to mention %q", tc.format, got, tc.want)
		}
	}
}

// TestExportPPTXDeniedForFreeAndPro pins FEAT-037's gate (monetization.md row 120: PowerPoint is
// Team only): Free AND Pro users get a 403 upgrade_required, never the deck.
func TestExportPPTXDeniedForFreeAndPro(t *testing.T) {
	for _, tier := range []domain.SubscriptionTier{domain.TierFree, domain.TierPro} {
		handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
		seedExportFixture(workspaces, reports, steps)
		users.tiers["user-1"] = tier

		req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pptx", nil, "user-1", tier)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("tier %s: status = %d, want %d, body = %s", tier, rec.Code, http.StatusForbidden, rec.Body.String())
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("tier %s: decoding body: %v", tier, err)
		}
		if body["code"] != "upgrade_required" {
			t.Errorf("tier %s: code = %q, want upgrade_required", tier, body["code"])
		}
		if audit.count() != 0 {
			t.Errorf("tier %s: a denied export must not write an audit entry", tier)
		}
	}
}

// TestExportPPTXForTeamSucceeds is the FEAT-037 happy path: a Team user exports a real, standard
// .pptx (a zip whose parts include the OOXML presentation + slides), with an audit entry. The chart
// slide (slide 3 for the bar-chart fixture) carries native editable shapes in the chart palette.
func TestExportPPTXForTeamSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	users.tiers["user-1"] = domain.TierTeam

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pptx", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
		t.Errorf("Content-Type = %q, want the pptx mime type", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, ".pptx\"") {
		t.Errorf("Content-Disposition = %q, want a .pptx attachment", cd)
	}

	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("PK\x03\x04")) {
		t.Fatalf("pptx body does not start with the zip magic (got %d bytes)", len(body))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("opening pptx as zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"[Content_Types].xml", "ppt/presentation.xml", "ppt/slideMasters/slideMaster1.xml",
		"ppt/slides/slide1.xml", "ppt/slides/slide2.xml", "ppt/slides/slide3.xml", "ppt/slides/slide4.xml", "ppt/slides/slide5.xml"} {
		if !names[want] {
			t.Errorf("pptx missing part %q", want)
		}
	}

	// The bar-chart fixture's chart slide (3) draws native rect shapes in chart-cat-1 (#1D4ED8).
	chartSlide := readZipEntry(t, zr, "ppt/slides/slide3.xml")
	if !strings.Contains(chartSlide, `prstGeom prst="rect"`) {
		t.Error("chart slide has no native rectangle shapes (bars must be editable rects)")
	}
	if !strings.Contains(chartSlide, `srgbClr val="1D4ED8"`) {
		t.Error("chart slide does not use the chart-cat-1 light token for the first bar")
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// readZipEntry opens a zip entry and returns its contents as a string.
func readZipEntry(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(b)
	}
	t.Fatalf("zip entry %q not found", name)
	return ""
}

// TestExportPPTXDegradesWithoutExecuteOutput pins the deck's honest degradation when the execute
// output is gone: the deck still exports (title, narrative, a stated note, provenance), never a
// fabricated chart — the same source-unavailable honesty as the PDF.
func TestExportPPTXDegradesWithoutExecuteOutput(t *testing.T) {
	handler, workspaces, reports, _, users, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-1", Title: "Narrative only",
		Narrative: "The numbers are in the text.", ChartSpec: domain.ChartSpec{ChartType: domain.ChartBar},
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}
	users.tiers["user-1"] = domain.TierTeam

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pptx", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("opening pptx as zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["ppt/slides/slide3.xml"] {
		t.Fatalf("expected a 4-slide deck (title, narrative, note, provenance), got %d slides", countSlides(names))
	}
	if got := readZipEntry(t, zr, "ppt/slides/slide3.xml"); !strings.Contains(got, "no longer available") {
		t.Error("the note slide does not state the chart gap honestly")
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// countSlides counts the slideN.xml parts in a deck's part map.
func countSlides(names map[string]bool) int {
	n := 0
	for name := range names {
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
			n++
		}
	}
	return n
}

// TestExportMarkdownForProSucceeds is the FEAT-038 happy path: a Pro user exports the report as
// Markdown with the narrative structure preserved, the chart embedded as a base64 data-URI image,
// reconciliation bullets, and an audit entry.
func TestExportMarkdownForProSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, prov := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	seedExportProvenance(reports, prov)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=markdown", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	got := rec.Body.String()
	for _, want := range []string{
		"# Customers by region",
		"North leads the count with 10 customers.",
		"![Customers by region](data:image/svg+xml;base64,",
		"## Reconciliation",
		"Rows read: 100",
		"excluded 31% of rows where region was null",
		"**North leads the count with 10 customers.** (10) — source rows: 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown body missing %q", want)
		}
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportMarkdownDeniedForFree pins FEAT-038's gate (monetization.md row 121: Markdown/HTML is
// Pro/Team).
func TestExportMarkdownDeniedForFree(t *testing.T) {
	handler, workspaces, reports, steps, _, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=markdown", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "upgrade_required" {
		t.Errorf("code = %q, want upgrade_required", body["code"])
	}
	if audit.count() != 0 {
		t.Error("a denied export must not write an audit entry")
	}
}

// TestExportHTMLForProSucceeds is the FEAT-038 HTML branch: a self-contained HTML document with the
// narrative in semantic paragraphs and the chart as inline SVG.
func TestExportHTMLForProSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, prov := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	seedExportProvenance(reports, prov)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=html", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	got := rec.Body.String()
	for _, want := range []string{
		"<!doctype html>",
		"<h1>Customers by region</h1>",
		"<p>North leads the count with 10 customers.</p>",
		"<svg",
		"viewBox=\"0 0 640 320\"",
		"<h2>Reconciliation</h2>",
		"<strong>North leads the count with 10 customers.</strong> (10)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("html body missing %q", want)
		}
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportHTMLDegradesWithoutExecuteOutput pins the HTML export's honest chart degradation: the
// document still exports with a note in place of the chart, never a fabricated graphic.
func TestExportHTMLDegradesWithoutExecuteOutput(t *testing.T) {
	handler, workspaces, reports, _, users, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-1", Title: "Narrative only",
		Narrative: "The numbers are in the text.", ChartSpec: domain.ChartSpec{ChartType: domain.ChartBar},
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=html", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, "no longer available") {
		t.Error("html body does not state the chart gap honestly")
	}
	if strings.Contains(got, "<svg") {
		t.Error("html body fabricates a chart when the execute output is gone")
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportJSONForProSucceeds is the FEAT-039 happy path: a Pro user exports the report as JSON
// following the documented schema — envelope, report (narrative + chart spec + expanded provenance
// links), and the raw execute output — with an audit entry.
func TestExportJSONForProSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, prov := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	seedExportProvenance(reports, prov)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=json", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var out reportExportJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding json export: %v", err)
	}
	if out.SchemaVersion != reportJSONSchemaVersion {
		t.Errorf("schema_version = %d, want %d", out.SchemaVersion, reportJSONSchemaVersion)
	}
	if out.Report.Title != "Customers by region" || !strings.Contains(out.Report.Narrative, "North leads") {
		t.Errorf("report = %+v, want title + narrative", out.Report)
	}
	if out.Report.ChartSpec.ChartType != domain.ChartBar || out.Report.ChartSpec.YAxisColumn != "cnt" {
		t.Errorf("chart_spec = %+v, want bar/cnt", out.Report.ChartSpec)
	}
	if len(out.Report.ProvenanceLinks) != 1 || out.Report.ProvenanceLinks[0].ClaimText == "" ||
		out.Report.ProvenanceLinks[0].SourceQuery == "" || len(out.Report.ProvenanceLinks[0].SourceRowIDs) != 1 {
		t.Errorf("provenance_links = %+v, want the expanded claim object", out.Report.ProvenanceLinks)
	}
	if out.ExecuteOutput == nil {
		t.Fatal("execute_output is nil, want the raw result set")
	}
	if len(out.ExecuteOutput.Columns) != 2 || len(out.ExecuteOutput.Rows) != 2 || out.ExecuteOutput.RowCount != 2 {
		t.Errorf("execute_output = %+v, want 2 columns / 2 rows / row_count 2", out.ExecuteOutput)
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportJSONDeniedForFree pins FEAT-039's gate (monetization.md row 122: JSON is Pro/Team).
func TestExportJSONDeniedForFree(t *testing.T) {
	handler, workspaces, reports, steps, _, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=json", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "upgrade_required" {
		t.Errorf("code = %q, want upgrade_required", body["code"])
	}
	if audit.count() != 0 {
		t.Error("a denied export must not write an audit entry")
	}
}

// TestExportJSONDegradesWithoutExecuteOutput pins the JSON export's honest degradation: the report
// itself (narrative, chart spec, provenance) still exports with execute_output null — never an
// error and never a fabricated result set.
func TestExportJSONDegradesWithoutExecuteOutput(t *testing.T) {
	handler, workspaces, reports, _, users, audit, prov := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-1", Title: "Narrative only",
		Narrative: "The numbers are in the text.", ChartSpec: domain.ChartSpec{ChartType: domain.ChartBar},
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}
	seedExportProvenance(reports, prov)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=json", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var out reportExportJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding json export: %v", err)
	}
	if out.ExecuteOutput != nil {
		t.Error("execute_output must be null when the result data is gone")
	}
	if !strings.Contains(out.Report.Narrative, "The numbers are in the text") {
		t.Errorf("report narrative missing from degraded export: %+v", out.Report)
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportImageForProSucceeds is the FEAT-040 happy path: a Pro user exports the rendered chart as
// a standalone SVG image that mirrors ChartRenderer's geometry (640x320 viewBox) and the chart
// palette — with an audit entry.
func TestExportImageForProSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=image", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, "-chart.svg\"") {
		t.Errorf("Content-Disposition = %q, want a -chart.svg attachment", cd)
	}
	got := rec.Body.String()
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		`viewBox="0 0 640 320"`,
		`<rect`,          // the bar chart's bars are real rects
		`fill="#1D4ED8"`, // chart-cat-1 light token, matching the in-app renderer's palette
	} {
		if !strings.Contains(got, want) {
			t.Errorf("svg body missing %q", want)
		}
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestExportImageDeniedForFree pins FEAT-040's gate (monetization.md row 123: Image is Pro/Team).
func TestExportImageDeniedForFree(t *testing.T) {
	handler, workspaces, reports, steps, _, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=image", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "upgrade_required" {
		t.Errorf("code = %q, want upgrade_required", body["code"])
	}
	if audit.count() != 0 {
		t.Error("a denied export must not write an audit entry")
	}
}

// TestExportImageResultDataUnavailable pins the honest failure for the image export when the run's
// execute step is gone: a 409 result_data_unavailable (the chart IS the execute output, so there is
// nothing honest to export), never a 500 and never a fabricated image.
func TestExportImageResultDataUnavailable(t *testing.T) {
	handler, workspaces, reports, _, users, audit, _ := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-1", Title: "t", Narrative: "n",
		ProvenanceLinks: []string{}, ReconciliationSummary: domain.ReconciliationSummary{},
	}
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=image", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "result_data_unavailable" {
		t.Errorf("code = %q, want result_data_unavailable", body["code"])
	}
	if audit.count() != 0 {
		t.Error("an unexportable export must not write an audit entry")
	}
}

// TestExportTableChartImageSucceeds pins the FEAT-040 fallback for a table-type chart spec: the
// "rendered chart" in-app is the data table, so the image export renders the result table as SVG
// rather than failing the row the dialog showed (screens/11's "shown and then failing" edge case).
func TestExportTableChartImageSucceeds(t *testing.T) {
	handler, workspaces, reports, steps, users, audit, _ := newExportTestServer(t)
	seedExportFixture(workspaces, reports, steps)
	report := reports.reports["report-1"]
	report.ChartSpec = domain.ChartSpec{ChartType: domain.ChartTable, Title: "Region counts"}
	reports.reports["report-1"] = report
	users.tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=image", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, "<svg") || !strings.Contains(got, "region") || !strings.Contains(got, "North") {
		t.Errorf("table-chart svg body missing the table content: %s", got)
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

// TestBuildChartSVGMirrorsChartRenderer pins the FEAT-040 geometry contract at the builder level:
// the SVG's category/series/scale math matches what the in-app ChartRenderer computes for the same
// spec + facts (bar positions, colors per series, tick formatting).
func TestBuildChartSVGMirrorsChartRenderer(t *testing.T) {
	spec := domain.ChartSpec{ChartType: domain.ChartBar, XAxisColumn: "region", YAxisColumn: "cnt", SeriesColumn: "yr", Title: "By year"}
	exec := domain.ExecuteStepOutput{
		Columns:  []string{"region", "cnt", "yr"},
		Rows:     [][]any{{"North", 10.0, "2024"}, {"North", 20.0, "2025"}, {"South", 8.0, "2024"}},
		RowCount: 3,
	}

	body, err := buildChartSVG(spec, exec)
	if err != nil {
		t.Fatalf("buildChartSVG: %v", err)
	}
	got := string(body)

	// Two series → the second series ("2025") uses chart-cat-2 (#B45309); both are present.
	if !strings.Contains(got, `fill="#1D4ED8"`) || !strings.Contains(got, `fill="#B45309"`) {
		t.Errorf("svg body missing expected series colors: %s", got)
	}
	// scaleMax is 20 → the 2025 North bar is full height; ticks are 0..20.
	if !strings.Contains(got, `>20<`) {
		t.Errorf("svg body missing the scaleMax tick label: %s", got)
	}
}
