package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeRunRepo is an in-memory RunStore for the runs endpoints.
type fakeRunRepo struct {
	mu   sync.Mutex
	runs map[string]domain.Run
}

func newFakeRunRepo() *fakeRunRepo {
	return &fakeRunRepo{runs: make(map[string]domain.Run)}
}

func (f *fakeRunRepo) Create(_ context.Context, run domain.Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[run.ID] = run
	return nil
}

func (f *fakeRunRepo) GetByID(_ context.Context, id string) (domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return domain.Run{}, errTestRunNotFound
	}
	return run, nil
}

// ListByParentRun mirrors RunRepository.ListByParentRun for the fork tree (FEAT-025): every run
// whose parent_run_id matches, in insertion order.
func (f *fakeRunRepo) ListByParentRun(_ context.Context, parentRunID string) ([]domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Run
	for _, run := range f.runs {
		if run.ParentRunID == parentRunID {
			out = append(out, run)
		}
	}
	return out, nil
}

var errTestRunNotFound = &testRunNotFoundError{}

type testRunNotFoundError struct{}

func (e *testRunNotFoundError) Error() string { return "run not found" }

// fakeRunStepRepo is an in-memory RunStepStore.
type fakeRunStepRepo struct {
	mu    sync.Mutex
	steps map[string][]domain.Step
}

func newFakeRunStepRepo() *fakeRunStepRepo {
	return &fakeRunStepRepo{steps: make(map[string][]domain.Step)}
}

func (f *fakeRunStepRepo) ListByRun(_ context.Context, runID string) ([]domain.Step, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.steps[runID], nil
}

// fakeRunExecutor records every run it was asked to submit; the real executor runs a goroutine, so
// asserting Submit happened (and with what run) is the handler test's boundary.
type fakeRunExecutor struct {
	mu        sync.Mutex
	userIDs   []string
	runs      []domain.Run
	sqls      []string
	resumed   []string
	halted    []string
	amended   []amendedRequest
	clarified []clarifiedRequest
}

type amendedRequest struct {
	runID    string
	stepName domain.StepName
	input    string
}

type clarifiedRequest struct {
	runID  string
	answer string
}

func (f *fakeRunExecutor) Submit(userID string, run domain.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.runs = append(f.runs, run)
}

// SubmitWithSQL records the submitted run AND the edited SQL it was seeded with, mirroring
// handleCreateRun's FEAT-018 branch (sql present → this path; blank/absent → Submit).
func (f *fakeRunExecutor) SubmitWithSQL(userID string, run domain.Run, sql string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.runs = append(f.runs, run)
	f.sqls = append(f.sqls, sql)
}

// Resume records the resume request (FEAT-022); the real executor runs a goroutine, so asserting the
// request reached it is the handler test's boundary.
func (f *fakeRunExecutor) Resume(userID string, runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.resumed = append(f.resumed, runID)
}

// Halt records the halt request (FEAT-024); the real executor runs a goroutine, so asserting the
// request reached it is the handler test's boundary.
func (f *fakeRunExecutor) Halt(userID string, runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.halted = append(f.halted, runID)
}

// AmendAndResume records the amend request (FEAT-024); the real executor runs a goroutine, so
// asserting the request reached it (and with what step/input) is the handler test's boundary.
func (f *fakeRunExecutor) AmendAndResume(userID string, runID string, stepName domain.StepName, input string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.amended = append(f.amended, amendedRequest{runID: runID, stepName: stepName, input: input})
}

// Clarify records the clarify request (FEAT-033); the real executor runs a goroutine, so asserting
// the request reached it (and with what answer) is the handler test's boundary.
func (f *fakeRunExecutor) Clarify(userID string, runID string, answer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.clarified = append(f.clarified, clarifiedRequest{runID: runID, answer: answer})
}

func (f *fakeRunExecutor) submitted() []domain.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Run, len(f.runs))
	copy(out, f.runs)
	return out
}

func (f *fakeRunExecutor) submittedSQL() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sqls))
	copy(out, f.sqls)
	return out
}

func (f *fakeRunExecutor) haltedRuns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.halted))
	copy(out, f.halted)
	return out
}

func (f *fakeRunExecutor) amendedRuns() []amendedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]amendedRequest, len(f.amended))
	copy(out, f.amended)
	return out
}

func (f *fakeRunExecutor) clarifiedRuns() []clarifiedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]clarifiedRequest, len(f.clarified))
	copy(out, f.clarified)
	return out
}

// newRunsTestServer wires the run endpoints' fakes plus the workspace store the ownership middleware
// needs.
func newRunsTestServer(t *testing.T) (*Server, *fakeWorkspaceStore, *fakeRunRepo, *fakeRunStepRepo, *fakeRunExecutor) {
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
	executor := &fakeRunExecutor{}

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret:  testAuthSecret,
		Workspaces:  workspaces,
		Users:       newFakeUserTierLookup(),
		Runs:        runs,
		RunSteps:    steps,
		RunExecutor: executor,
	})
	return server, workspaces, runs, steps, executor
}

func TestCreateRunRequiresAuthentication(t *testing.T) {
	server, _, _, _, executor := newRunsTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws-1/runs", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(executor.submitted()) != 0 {
		t.Error("executor was submitted despite no auth")
	}
}

func TestCreateRunSucceeds(t *testing.T) {
	server, workspaces, _, _, executor := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "  How many customers per region?  "}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var body struct {
		Run struct {
			ID           string `json:"id"`
			WorkspaceID  string `json:"workspace_id"`
			QuestionText string `json:"question_text"`
			Status       string `json:"status"`
			CreatedAt    string `json:"created_at"`
		} `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Run.ID == "" {
		t.Error("run id is empty")
	}
	if body.Run.WorkspaceID != "ws-1" {
		t.Errorf("workspace_id = %q, want ws-1", body.Run.WorkspaceID)
	}
	if body.Run.QuestionText != "How many customers per region?" {
		t.Errorf("question_text = %q, want the trimmed question", body.Run.QuestionText)
	}
	if body.Run.Status != string(domain.RunQueued) {
		t.Errorf("status = %q, want %q", body.Run.Status, domain.RunQueued)
	}

	// The executor must be handed the exact persisted run, as the initiating user.
	submitted := executor.submitted()
	if len(submitted) != 1 {
		t.Fatalf("executor submissions = %d, want 1", len(submitted))
	}
	if submitted[0].ID != body.Run.ID || submitted[0].Status != domain.RunQueued {
		t.Errorf("submitted run = %+v, want the queued run returned in the response", submitted[0])
	}
	if len(executor.userIDs) != 1 || executor.userIDs[0] != "user-1" {
		t.Errorf("submitted user ids = %v, want [user-1]", executor.userIDs)
	}
}

// TestCreateRunWithEditedSQLPins the FEAT-018 fetch: a create patch with the edited SQL must route
// the run through SubmitWithSQL (never Submit), passing question_text and the SQL verbatim, and still
// return the persisted queued run.
func TestCreateRunWithEditedSQLUsesSeededPath(t *testing.T) {
	server, workspaces, _, _, executor := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	edited := "SELECT region, SUM(amount) FROM dataset_sales_csv_d5f3e2a1 GROUP BY region"
	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "How many customers per region?", "sql": edited}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	// The seeded submission carries the run AND the exact SQL; nothing was submitted via Submit.
	sqls := executor.submittedSQL()
	if len(sqls) != 1 || sqls[0] != edited {
		t.Fatalf("seeded SQL submissions = %v, want [%q]", sqls, edited)
	}
	submitted := executor.submitted()
	if len(submitted) != 1 {
		t.Fatalf("executor submissions = %d, want exactly the one seeded run", len(submitted))
	}
	if submitted[0].QuestionText != "How many customers per region?" {
		t.Errorf("submitted question = %q", submitted[0].QuestionText)
	}
	if strings.Contains(rec.Body.String(), `"sql"`) {
		t.Errorf("response body leaks the edited SQL field: %s", rec.Body.String())
	}
}

func TestCreateRunRejectsBlankQuestion(t *testing.T) {
	server, workspaces, _, _, executor := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "   "}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "invalid_question" {
		t.Errorf("code = %q, want invalid_question", body["code"])
	}
	if len(executor.submitted()) != 0 {
		t.Error("executor was submitted for a blank question")
	}
}

func TestCreateRunRejectsNonOwner(t *testing.T) {
	server, workspaces, _, _, executor := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "question"}, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
	if len(executor.submitted()) != 0 {
		t.Error("executor was submitted for a workspace the user does not own")
	}
}

/* -------------------------------------------------------------------------------------------- */
/* FEAT-020 capture hook                                                                         */
/* -------------------------------------------------------------------------------------------- */

// newRunsTestServerWithCorrections is newRunsTestServer plus an in-memory SemanticCorrectionStore,
// for asserting FEAT-020's capture of an edited-SQL run.
func newRunsTestServerWithCorrections(t *testing.T, corrections *fakeSemanticCorrectionStore) (*Server, *fakeWorkspaceStore, *fakeRunExecutor) {
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
	executor := &fakeRunExecutor{}

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret:          testAuthSecret,
		Workspaces:          workspaces,
		Users:               newFakeUserTierLookup(),
		Runs:                newFakeRunRepo(),
		RunSteps:            newFakeRunStepRepo(),
		RunExecutor:         executor,
		SemanticCorrections: corrections,
	})
	return server, workspaces, executor
}

// TestCreateRunWithEditedSQLCapturesCorrection pins FEAT-020's capture: a create with the user's
// edited SQL (FEAT-018) both seeds a new run AND persists a durable Correction row attributing the
// edit — corrected logic = the SQL, workspace scoped, attributed to the NEW run, with a plain-
// language description. AC1's durable-preference shape and AC2's attribution.
func TestCreateRunWithEditedSQLCapturesCorrection(t *testing.T) {
	corrections := newFakeSemanticCorrectionStore()
	server, workspaces, executor := newRunsTestServerWithCorrections(t, corrections)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	edited := "SELECT region, SUM(amount) FROM dataset_sales_csv_d5f3e2a1 GROUP BY region"
	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "How many customers per region?", "sql": edited}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	// SubmitWithSQL still fired for the seeded run.
	if sqls := executor.submittedSQL(); len(sqls) != 1 || sqls[0] != edited {
		t.Fatalf("seeded SQL submissions = %v, want [%q]", sqls, edited)
	}

	// Exactly one correction captured, its fields as decided (see captureCorrection's doc).
	got := corrections.byWorkspace["ws-1"]
	if len(got) != 1 {
		t.Fatalf("captured corrections = %d, want 1 (list = %+v)", len(got), got)
	}
	c := got[0]
	if c.WorkspaceID != "ws-1" {
		t.Errorf("workspace_id = %q, want ws-1", c.WorkspaceID)
	}
	if c.CorrectedLogic != edited {
		t.Errorf("corrected_logic = %q, want the edited SQL verbatim", c.CorrectedLogic)
	}
	if c.OriginatingRunID != body.Run.ID {
		t.Errorf("originating_run_id = %q, want the new run's id %q", c.OriginatingRunID, body.Run.ID)
	}
	if c.Description == "" {
		t.Error("description is empty; a correction must carry a plain-language summary")
	}
	// OriginalLogic is unknown at capture (the request does not carry the prior SQL) — stored empty.
	if c.OriginalLogic != "" {
		t.Errorf("original_logic = %q, want empty (not carried by the create request)", c.OriginalLogic)
	}
}

// TestCreateRunWithoutSQLCapturesNoCorrection pins that the ordinary question → plan → generate_sql
// path (no edited SQL) must not fabricate a correction.
func TestCreateRunWithoutSQLCapturesNoCorrection(t *testing.T) {
	corrections := newFakeSemanticCorrectionStore()
	server, workspaces, executor := newRunsTestServerWithCorrections(t, corrections)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "How many customers?"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	if len(executor.submitted()) != 1 {
		t.Fatalf("executor submissions = %d, want the single normal Submit", len(executor.submitted()))
	}
	if len(corrections.byWorkspace["ws-1"]) != 0 {
		t.Error("a correction was captured for a run with no edited SQL")
	}
}

// TestCreateRunContinuesWhenCorrectionCaptureFails pins that FEAT-020's capture is best-effort: a
// failed correction write must not fail the run the user asked for (still 202, still submitted).
func TestCreateRunContinuesWhenCorrectionCaptureFails(t *testing.T) {
	corrections := newFakeSemanticCorrectionStore()
	corrections.createErr = errCapturingCorrection
	server, workspaces, executor := newRunsTestServerWithCorrections(t, corrections)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs",
		map[string]string{"question_text": "How many customers?", "sql": "SELECT 1"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even when correction capture fails; body = %s", rec.Code, rec.Body.String())
	}
	if sqls := executor.submittedSQL(); len(sqls) != 1 {
		t.Fatalf("seeded SQL submissions = %d, want 1 despite a failed capture", len(sqls))
	}
}

var errCapturingCorrection = errors.New("simulated correction capture failure")

func TestGetRunRequiresAuthentication(t *testing.T) {
	server, _, _, _, _ := newRunsTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1/runs/run-1", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestGetRunSucceeds(t *testing.T) {
	server, workspaces, runs, steps, _ := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	started := time.Now().UTC()
	runs.runs["run-1"] = domain.Run{
		ID: "run-1", WorkspaceID: "ws-1", QuestionText: "How many customers?",
		Status: domain.RunComplete, CreatedAt: started, CompletedAt: &started,
	}
	steps.steps["run-1"] = []domain.Step{
		{ID: "step-1", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete, Output: `{"answerable":true}`},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/runs/run-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Run struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			ParentID string `json:"parent_run_id"`
		} `json:"run"`
		Steps []struct {
			ID       string `json:"id"`
			StepName string `json:"step_name"`
			Status   string `json:"status"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Run.ID != "run-1" || body.Run.Status != string(domain.RunComplete) {
		t.Errorf("run = %+v", body.Run)
	}
	if len(body.Steps) != 1 || body.Steps[0].StepName != string(domain.StepPlan) || body.Steps[0].Status != string(domain.StepComplete) {
		t.Errorf("steps = %+v, want the plan step", body.Steps)
	}
}

func TestGetRunReturnsNotFoundForUnknownRun(t *testing.T) {
	server, workspaces, _, _, _ := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/runs/nope", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestGetRunReturnsNotFoundForCrossWorkspaceRun pins the ownership fix: a run belonging to a
// different workspace than the URL's must read as a plain 404, never leak the run's existence or
// contents (non-enumeration).
func TestGetRunReturnsNotFoundForCrossWorkspaceRun(t *testing.T) {
	server, workspaces, runs, _, _ := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	runs.runs["run-other"] = domain.Run{ID: "run-other", WorkspaceID: "ws-other", QuestionText: "secret"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/runs/run-other", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestGetRunRejectsNonOwner(t *testing.T) {
	server, workspaces, runs, _, _ := newRunsTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}
	runs.runs["run-1"] = domain.Run{ID: "run-1", WorkspaceID: "ws-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/runs/run-1", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
}
