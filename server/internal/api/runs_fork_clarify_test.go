package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// newRunsForkTestServer wires the run endpoints' fakes plus reports + corrections so the fork
// (FEAT-025), follow-up (FEAT-021), and clarify (FEAT-033) flows can be exercised end to end.
func newRunsForkTestServer(t *testing.T, corrections *fakeSemanticCorrectionStore) (*Server, *fakeWorkspaceStore, *fakeRunRepo, *fakeRunStepRepo, *fakeReportRepo, *fakeRunExecutor) {
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
	runs := newFakeRunRepo()
	steps := newFakeRunStepRepo()
	reports := newFakeReportRepo()
	executor := &fakeRunExecutor{}
	// FEAT-021/FEAT-025 are Pro/Team (monetization.md); the acting user defaults to Pro so the
	// fork/follow-up tests exercise the feature, not the gate (the gate has its own tests).
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro

	deps := Dependencies{
		AuthSecret:  testAuthSecret,
		Workspaces:  workspaces,
		Users:       users,
		Runs:        runs,
		RunSteps:    steps,
		Reports:     reports,
		RunExecutor: executor,
	}
	// Only wire the corrections seam when a store is supplied. Assigning a nil *fake… into the
	// interface field would make a non-nil interface holding a nil pointer, defeating the handler's
	// `corrections != nil` guard.
	if corrections != nil {
		deps.SemanticCorrections = corrections
	}

	server := NewServer(cfg, logger, nil, deps)
	return server, workspaces, runs, steps, reports, executor
}

// TestCreateRunWithParentRunForks pins FEAT-025's fork-on-amendment HTTP contract: a create that
// carries parent_run_id creates a NEW run linked to that parent (original untouched) and hands the
// fork to the executor with the linkage already recorded.
func TestCreateRunWithParentRunForks(t *testing.T) {
	server, workspaces, runs, _, _, executor := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	runs.runs["run-parent"] = domain.Run{
		ID: "run-parent", WorkspaceID: "ws-1", QuestionText: "How many customers?",
		Status: domain.RunComplete, CreatedAt: time.Now().UTC(),
	}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]any{
			"question_text": "How many customers per region?",
			"sql":           "SELECT region, COUNT(*) FROM dataset_sales_csv_d5f3e2a1 GROUP BY region",
			"parent_run_id": "run-parent",
		}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}

	submitted := executor.submitted()
	if len(submitted) != 1 {
		t.Fatalf("executor submissions = %d, want 1", len(submitted))
	}
	if submitted[0].ParentRunID != "run-parent" {
		t.Errorf("submitted run parent_run_id = %q, want run-parent (the fork link)", submitted[0].ParentRunID)
	}
	// The parent run row is untouched.
	if runs.runs["run-parent"].Status != domain.RunComplete {
		t.Errorf("parent run mutated: %+v", runs.runs["run-parent"])
	}
}

// TestCreateRunWithParentRunFromAnotherWorkspaceRejected pins the non-enumeration stance for forks:
// a parent_run_id the caller cannot own reads as 404, never confirming the run's existence.
func TestCreateRunWithParentRunFromAnotherWorkspaceRejected(t *testing.T) {
	server, workspaces, runs, _, _, executor := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	runs.runs["run-other-ws"] = domain.Run{ID: "run-other-ws", WorkspaceID: "ws-other"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]any{"question_text": "How many?", "parent_run_id": "run-other-ws"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (non-enumeration), body = %s", rec.Code, rec.Body.String())
	}
	if len(executor.submitted()) != 0 {
		t.Error("executor was submitted for a cross-workspace parent")
	}
}

// TestCreateRunWithParentReportLinksFollowUp pins FEAT-021's compositional follow-up: a create that
// carries parent_report_id validates the report in this workspace, derives the run linkage from the
// report's source run, and creates a run that will materialize a linked report.
func TestCreateRunWithParentReportLinksFollowUp(t *testing.T) {
	server, workspaces, runs, _, reports, executor := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-source",
		Title: "Q1 sales", CreatedAt: time.Now().UTC(),
	}
	// The source run exists too (a follow-up is anchored to it).
	runs.runs["run-source"] = domain.Run{
		ID: "run-source", WorkspaceID: "ws-1", QuestionText: "Q1 sales?",
		Status: domain.RunComplete, CreatedAt: time.Now().UTC(),
	}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]any{
			"question_text":    "break this down by month",
			"parent_report_id": "report-1",
		}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}

	submitted := executor.submitted()
	if len(submitted) != 1 {
		t.Fatalf("executor submissions = %d, want 1", len(submitted))
	}
	// The run linkage derives from the report's own source run.
	if submitted[0].ParentRunID != "run-source" {
		t.Errorf("submitted run parent_run_id = %q, want run-source (the report's run)", submitted[0].ParentRunID)
	}
}

// TestCreateRunWithUnknownParentReportRejected pins that a follow-up on a report the caller cannot
// own (or that does not exist) is a 404, never a silent unlinked run.
func TestCreateRunWithUnknownParentReportRejected(t *testing.T) {
	server, workspaces, _, _, _, executor := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]any{"question_text": "follow up?", "parent_report_id": "no-such-report"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	if len(executor.submitted()) != 0 {
		t.Error("executor was submitted for an unknown parent report")
	}
}

// TestGetRunReturnsForkedRuns pins FEAT-025's branch/tree presentation data: the GET run response
// carries the runs forked from the viewed run, so the Run Viewer can render the tree in one round
// trip.
func TestGetRunReturnsForkedRuns(t *testing.T) {
	server, workspaces, runs, steps, _, _ := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	started := time.Now().UTC()
	runs.runs["run-1"] = domain.Run{
		ID: "run-1", WorkspaceID: "ws-1", QuestionText: "How many?",
		Status: domain.RunComplete, CreatedAt: started, CompletedAt: &started,
	}
	runs.runs["run-1-fork"] = domain.Run{
		ID: "run-1-fork", WorkspaceID: "ws-1", QuestionText: "How many per region?",
		Status: domain.RunComplete, ParentRunID: "run-1", CreatedAt: started.Add(time.Minute),
	}
	steps.steps["run-1"] = []domain.Step{}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/runs/run-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ForkedRuns []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"forked_runs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.ForkedRuns) != 1 || body.ForkedRuns[0].ID != "run-1-fork" {
		t.Errorf("forked_runs = %+v, want [run-1-fork]", body.ForkedRuns)
	}
}

// TestClarifyRunSucceeds pins FEAT-033's HTTP contract: answering a run that paused with a pending
// clarifying question persists the clarification as a durable Correction (so the same ambiguity is
// not re-asked in the same context — AC3) and hands the answer to the executor.
func TestClarifyRunSucceeds(t *testing.T) {
	corrections := newFakeSemanticCorrectionStore()
	server, workspaces, runs, steps, _, executor := newRunsForkTestServer(t, corrections)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	runs.runs["run-1"] = domain.Run{
		ID: "run-1", WorkspaceID: "ws-1", QuestionText: "Top customers?",
		Status: domain.RunHalted, CreatedAt: time.Now().UTC(),
	}
	steps.steps["run-1"] = []domain.Step{{
		ID: "plan-step", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete,
		Output:         `{"answerable":true,"needs_clarification":true,"clarification_question":"By revenue or by count?","clarification_options":["By revenue","By count"],"ambiguity_key":"metric_undefined"}`,
		IdempotencyKey: "run-1:plan",
	}}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/clarify",
		map[string]string{"answer": "By revenue"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}

	clarified := executor.clarifiedRuns()
	if len(clarified) != 1 || clarified[0].runID != "run-1" || clarified[0].answer != "By revenue" {
		t.Fatalf("clarify requests = %+v, want [{run-1 By revenue}]", clarified)
	}

	// AC3: the clarification is persisted as a durable correction keyed to the ambiguity.
	got := corrections.byWorkspace["ws-1"]
	if len(got) != 1 {
		t.Fatalf("clarification corrections = %d, want 1", len(got))
	}
	if got[0].CorrectedLogic != "By revenue" {
		t.Errorf("correction corrected_logic = %q, want the answer", got[0].CorrectedLogic)
	}
	if !strings.Contains(got[0].Description, "metric_undefined") {
		t.Errorf("correction description = %q, want it to carry the ambiguity key", got[0].Description)
	}
	if got[0].OriginatingRunID != "run-1" {
		t.Errorf("correction originating_run_id = %q, want run-1", got[0].OriginatingRunID)
	}
}

// TestClarifyRunRejectsRunNotAwaitingClarification pins the 409 guard: a halted run whose plan did
// not ask a clarifying question (a manual halt, FEAT-024) cannot be answered — no fabricated
// resolution, no executor call, no correction.
func TestClarifyRunRejectsRunNotAwaitingClarification(t *testing.T) {
	corrections := newFakeSemanticCorrectionStore()
	server, workspaces, runs, steps, _, executor := newRunsForkTestServer(t, corrections)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	runs.runs["run-1"] = domain.Run{
		ID: "run-1", WorkspaceID: "ws-1", QuestionText: "How many?",
		Status: domain.RunHalted, CreatedAt: time.Now().UTC(),
	}
	steps.steps["run-1"] = []domain.Step{{
		ID: "plan-step", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete,
		Output: `{"answerable":true,"approach":"group by region"}`, IdempotencyKey: "run-1:plan",
	}}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/clarify",
		map[string]string{"answer": "By revenue"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	if len(executor.clarifiedRuns()) != 0 {
		t.Error("executor was asked to clarify a run that is not awaiting clarification")
	}
	if len(corrections.byWorkspace["ws-1"]) != 0 {
		t.Error("a correction was persisted for a run that was not awaiting clarification")
	}
}

// TestClarifyRunRejectsBlankAnswer pins the request guard: an empty answer is a 400, never handed
// to the executor.
func TestClarifyRunRejectsBlankAnswer(t *testing.T) {
	server, workspaces, runs, steps, _, executor := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	runs.runs["run-1"] = domain.Run{
		ID: "run-1", WorkspaceID: "ws-1", QuestionText: "Top customers?",
		Status: domain.RunHalted, CreatedAt: time.Now().UTC(),
	}
	steps.steps["run-1"] = []domain.Step{{
		ID: "plan-step", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete,
		Output:         `{"answerable":true,"needs_clarification":true,"clarification_question":"By revenue or by count?","ambiguity_key":"metric_undefined"}`,
		IdempotencyKey: "run-1:plan",
	}}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/clarify",
		map[string]string{"answer": "   "}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if len(executor.clarifiedRuns()) != 0 {
		t.Error("executor was asked to clarify with a blank answer")
	}
}

// newRunsForkTestServerWithTier is newRunsForkTestServer plus an explicitly configured user tier for
// the acting user, for asserting FEAT-021/FEAT-025's Pro/Team tier gate against the AUTHORITATIVE
// store lookup (the cookie's tier claim is display-only and must not open the gate).
func newRunsForkTestServerWithTier(t *testing.T, tier domain.SubscriptionTier) (*Server, *fakeWorkspaceStore, *fakeRunExecutor, *fakeUserTierLookup) {
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
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = tier
	executor := &fakeRunExecutor{}

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret:  testAuthSecret,
		Workspaces:  workspaces,
		Users:       users,
		Runs:        newFakeRunRepo(),
		RunSteps:    newFakeRunStepRepo(),
		Reports:     newFakeReportRepo(),
		RunExecutor: executor,
	})
	return server, workspaces, executor, users
}

// TestCreateRunWithParentContextRejectedOnFreeTier pins monetization.md's gate: compositional
// follow-ups (FEAT-021) and fork-on-amendment (FEAT-025) are Pro/Team, enforced server-side against
// the store's authoritative tier — a Free user gets 403 upgrade_required even though their session
// cookie's tier claim could say anything.
func TestCreateRunWithParentContextRejectedOnFreeTier(t *testing.T) {
	server, workspaces, executor, _ := newRunsForkTestServerWithTier(t, domain.TierFree)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]any{
			"question_text":    "break this down by month",
			"parent_report_id": "report-1",
		}, "user-1", domain.TierPro /* cookie claim ≠ store tier — must NOT open the gate */)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 upgrade_required, body = %s", rec.Code, rec.Body.String())
	}
	if len(executor.submitted()) != 0 {
		t.Error("executor was submitted for a gated follow-up on the Free tier")
	}
}

// TestCreateRunWithParentContextAllowedOnProTier pins the gate's open side: a Pro user may fork or
// ask a follow-up.
func TestCreateRunWithParentContextAllowedOnProTier(t *testing.T) {
	server, workspaces, runs, _, reports, executor := newRunsForkTestServer(t, nil)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	reports.reports["report-1"] = domain.Report{
		ID: "report-1", WorkspaceID: "ws-1", RunID: "run-source",
		Title: "Q1 sales", CreatedAt: time.Now().UTC(),
	}
	runs.runs["run-source"] = domain.Run{
		ID: "run-source", WorkspaceID: "ws-1", QuestionText: "Q1 sales?",
		Status: domain.RunComplete, CreatedAt: time.Now().UTC(),
	}
	// A Pro-tier user in the authoritative store.
	server.deps.Users.(*fakeUserTierLookup).tiers["user-1"] = domain.TierPro

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]any{
			"question_text":    "break this down by month",
			"parent_report_id": "report-1",
		}, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	submitted := executor.submitted()
	if len(submitted) != 1 || submitted[0].ParentRunID != "run-source" {
		t.Fatalf("executor submissions = %+v, want one run linked to run-source", submitted)
	}
}

func TestClarifyRunRequiresAuthentication(t *testing.T) {
	server, _, _, _, _, executor := newRunsForkTestServer(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/clarify", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(executor.clarifiedRuns()) != 0 {
		t.Error("executor was asked to clarify without auth")
	}
}
