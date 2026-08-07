package api

import (
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
// api.go during assembly).
func newExportTestRouter(
	workspaces WorkspaceStore,
	reports ReportStore,
	steps RunStepStore,
	users UserTierLookup,
	audit ExportAuditWriter,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterExportRoutes(r, workspaces, reports, steps, users, audit,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

// newExportTestServer returns the router plus its fakes, so each test can seed state and assert
// against the same in-memory stores.
func newExportTestServer(t *testing.T) (http.Handler, *fakeWorkspaceStore, *fakeReportRepo, *fakeRunStepRepo, *fakeUserTierLookup, *fakeExportAuditWriter) {
	t.Helper()
	workspaces := newFakeWorkspaceStore()
	reports := newFakeReportRepo()
	steps := newFakeRunStepRepo()
	users := newFakeUserTierLookup()
	audit := &fakeExportAuditWriter{}
	return newExportTestRouter(workspaces, reports, steps, users, audit), workspaces, reports, steps, users, audit
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

// TestExportRequiresAuthentication pins that the export endpoint is behind the auth boundary like
// every other report endpoint.
func TestExportRequiresAuthentication(t *testing.T) {
	handler, _, _, _, _, _ := newExportTestServer(t)

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
	handler, workspaces, _, _, _, audit := newExportTestServer(t)
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
// the fixture seeds the workspace.
func TestExportInvalidFormat(t *testing.T) {
	handler, workspaces, _, _, _, audit := newExportTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/reports/report-1/export?format=pptx", nil, "user-1", domain.TierPro)
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
	handler, workspaces, _, _, _, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, users, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, _, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, users, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, users, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, _, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, users, _ := newExportTestServer(t)
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
	handler, workspaces, reports, _, users, audit := newExportTestServer(t)
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
	handler, workspaces, reports, _, users, audit := newExportTestServer(t)
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
	handler, workspaces, reports, steps, _, audit := newExportTestServer(t)
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
