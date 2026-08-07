package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
)

// newHaltAmendTestServer is newRunsTestServer with a live event hub wired, which is what gates the
// halt/amend endpoints' registration (RegisterRunRoutes registers them only when hub != nil).
func newHaltAmendTestServer(t *testing.T) (*Server, *fakeWorkspaceStore, *fakeRunRepo, *fakeRunExecutor) {
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
	executor := &fakeRunExecutor{}

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret:  testAuthSecret,
		Workspaces:  workspaces,
		Users:       newFakeUserTierLookup(),
		Runs:        runs,
		RunSteps:    newFakeRunStepRepo(),
		RunExecutor: executor,
		RunEvents:   events.NewHub(),
	})
	return server, workspaces, runs, executor
}

// seedOwnedRun stashes a run owned by user-1 in workspace ws-1.
func seedOwnedRun(runs *fakeRunRepo, status domain.RunStatus) {
	runs.runs["run-1"] = domain.Run{ID: "run-1", WorkspaceID: "ws-1", Status: status}
}

func TestHaltRunSucceeds(t *testing.T) {
	server, workspaces, runs, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	seedOwnedRun(runs, domain.RunRunning)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/halt",
		nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "halted" || body["run_id"] != "run-1" {
		t.Errorf("body = %v, want status=halted run_id=run-1", body)
	}
	halted := executor.haltedRuns()
	if len(halted) != 1 || halted[0] != "run-1" {
		t.Fatalf("executor halts = %v, want [run-1]", halted)
	}
}

func TestHaltRunRejectsNonOwner(t *testing.T) {
	server, workspaces, runs, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}
	seedOwnedRun(runs, domain.RunRunning)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/halt",
		nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
	if len(executor.haltedRuns()) != 0 {
		t.Error("executor halted a run in a workspace the user does not own")
	}
}

func TestHaltRunNotFound(t *testing.T) {
	server, workspaces, _, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/nope/halt",
		nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if len(executor.haltedRuns()) != 0 {
		t.Error("executor halted an unknown run")
	}
}

func TestAmendRunSucceeds(t *testing.T) {
	server, workspaces, runs, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	seedOwnedRun(runs, domain.RunHalted)

	edited := "SELECT region, SUM(amount) AS total FROM dataset_sales_csv_d5f3e2a1 GROUP BY region"
	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/amend",
		map[string]string{"step_name": "generate_sql", "input": edited}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	amended := executor.amendedRuns()
	if len(amended) != 1 {
		t.Fatalf("executor amends = %v, want exactly 1", amended)
	}
	if amended[0].runID != "run-1" || amended[0].stepName != domain.StepGenerateSQL || amended[0].input != edited {
		t.Errorf("amend = %+v, want run-1/generate_sql/%q", amended[0], edited)
	}
}

func TestAmendRunRejectsBlankStep(t *testing.T) {
	server, workspaces, runs, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	seedOwnedRun(runs, domain.RunHalted)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/amend",
		map[string]string{"step_name": "", "input": "SELECT 1"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "invalid_step" {
		t.Errorf("code = %q, want invalid_step", body["code"])
	}
	if len(executor.amendedRuns()) != 0 {
		t.Error("executor amended for a blank step")
	}
}

func TestAmendRunRejectsEmptyInput(t *testing.T) {
	server, workspaces, runs, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	seedOwnedRun(runs, domain.RunHalted)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/amend",
		map[string]string{"step_name": "generate_sql", "input": "   "}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "invalid_input" {
		t.Errorf("code = %q, want invalid_input", body["code"])
	}
	if len(executor.amendedRuns()) != 0 {
		t.Error("executor amended for empty input")
	}
}

func TestAmendRunRejectsNonOwner(t *testing.T) {
	server, workspaces, runs, executor := newHaltAmendTestServer(t)
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}
	seedOwnedRun(runs, domain.RunHalted)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/runs/run-1/amend",
		map[string]string{"step_name": "generate_sql", "input": "SELECT 1"}, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-enumeration)", rec.Code, http.StatusNotFound)
	}
	if len(executor.amendedRuns()) != 0 {
		t.Error("executor amended a run in a workspace the user does not own")
	}
}
