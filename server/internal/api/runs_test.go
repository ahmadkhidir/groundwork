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
	mu      sync.Mutex
	userIDs []string
	runs    []domain.Run
	sqls    []string
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
