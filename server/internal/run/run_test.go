package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor polls cond until it is true or the deadline passes, failing the test.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

// ---- fakes for the Executor's narrow seams ----

type fakeRunStore struct {
	mu       sync.Mutex
	run      domain.Run
	statuses []domain.RunStatus
}

func (f *fakeRunStore) GetByID(_ context.Context, _ string) (domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run, nil
}

func (f *fakeRunStore) UpdateStatus(_ context.Context, _ string, status domain.RunStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run.Status = status
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeRunStore) current() domain.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run
}

func (f *fakeRunStore) allStatuses() []domain.RunStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.RunStatus, len(f.statuses))
	copy(out, f.statuses)
	return out
}

type fakeStepStore struct {
	mu    sync.Mutex
	steps []domain.Step
}

func (f *fakeStepStore) Create(_ context.Context, _ string, step domain.Step) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, step)
	return nil
}

func (f *fakeStepStore) Complete(_ context.Context, id, output string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.steps {
		if f.steps[i].ID == id {
			now := time.Now().UTC()
			f.steps[i].Status = domain.StepComplete
			f.steps[i].Output = output
			f.steps[i].CompletedAt = &now
			return nil
		}
	}
	return errors.New("step not found")
}

func (f *fakeStepStore) Fail(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.steps {
		if f.steps[i].ID == id {
			now := time.Now().UTC()
			f.steps[i].Status = domain.StepFailed
			f.steps[i].Output = reason
			f.steps[i].CompletedAt = &now
			return nil
		}
	}
	return errors.New("step not found")
}

func (f *fakeStepStore) all() []domain.Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Step, len(f.steps))
	copy(out, f.steps)
	return out
}

type fakeDatasetStore struct {
	datasets []domain.Dataset
}

func (f *fakeDatasetStore) ListByWorkspace(_ context.Context, _ string) ([]domain.Dataset, error) {
	return f.datasets, nil
}

type fakeColumnStore struct {
	byDataset map[string][]domain.Column
}

func (f *fakeColumnStore) ListByDataset(_ context.Context, datasetID string) ([]domain.Column, error) {
	return f.byDataset[datasetID], nil
}

type fakeObjectStore struct{}

func (f *fakeObjectStore) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("parquet-bytes")), nil
}

type fakeProvider struct {
	mu         sync.Mutex
	handle     engine.Handle
	acquireErr error
	releases   int
}

func (f *fakeProvider) Acquire(_ context.Context, _ string) (engine.Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	return f.handle, nil
}

func (f *fakeProvider) Release(_ engine.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return nil
}

func (f *fakeProvider) Status(_ context.Context, _ string) (engine.Status, error) {
	return engine.StatusReady, nil
}

type fakeHandle struct {
	mu             sync.Mutex
	execStatements []string
	rows           engine.Rows
}

func (f *fakeHandle) WorkspaceID() string { return "ws-1" }
func (f *fakeHandle) AcquiredAt() time.Time {
	return time.Now()
}

func (f *fakeHandle) Query(_ context.Context, _ string, _ ...any) (engine.Rows, error) {
	return f.rows, nil
}

func (f *fakeHandle) Exec(_ context.Context, sql string, _ ...any) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execStatements = append(f.execStatements, sql)
	return 0, nil
}

func (f *fakeHandle) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.execStatements))
	copy(out, f.execStatements)
	return out
}

type fakeRows struct {
	cols []string
	rows [][]any
	idx  int
}

func (f *fakeRows) Next() bool {
	if f.idx < len(f.rows) {
		return true
	}
	return false
}

func (f *fakeRows) Scan(dest ...any) error {
	for i, v := range f.rows[f.idx] {
		*(dest[i].(*any)) = v
	}
	f.idx++
	return nil
}

func (f *fakeRows) Columns() []string { return f.cols }
func (f *fakeRows) Err() error        { return nil }
func (f *fakeRows) Close() error      { return nil }

type fakeModelClient struct {
	mu    sync.Mutex
	queue []string
	calls int
	err   error
}

// enqueue appends a scripted response; each Complete call consumes the queue front.
func (f *fakeModelClient) enqueue(responses ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, responses...)
}

func (f *fakeModelClient) Complete(_ context.Context, _, _, _ string, _ model.Payload) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(f.queue) == 0 {
		return "", errors.New("no scripted response")
	}
	raw := f.queue[0]
	f.queue = f.queue[1:]
	return raw, nil
}

func (f *fakeModelClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeAudit struct {
	mu            sync.Mutex
	queryExecuted int
}

func (f *fakeAudit) WriteModelCall(context.Context, string, string, string) error { return nil }

func (f *fakeAudit) WriteQueryExecuted(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryExecuted++
	return nil
}

func (f *fakeAudit) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queryExecuted
}

type fakeRelationshipLister struct{}

func (f *fakeRelationshipLister) ListByWorkspace(_ context.Context, _ string) ([]domain.Relationship, error) {
	return nil, nil
}

type fakeMetricLister struct{}

func (f *fakeMetricLister) ListByWorkspace(_ context.Context, _ string) ([]domain.MetricDefinition, error) {
	return nil, nil
}

type fakeCorrectionLister struct{}

func (f *fakeCorrectionLister) ListByWorkspace(_ context.Context, _ string) ([]domain.Correction, error) {
	return nil, nil
}

// newTestExecutor wires a fully fake Executor plus all fakes back to the caller.
func newTestExecutor() (*Executor, *fakeRunStore, *fakeStepStore, *fakeHandle, *fakeModelClient, *fakeAudit) {
	runs := &fakeRunStore{}
	steps := &fakeStepStore{}
	handle := &fakeHandle{rows: &fakeRows{cols: []string{"region", "cnt"}, rows: [][]any{{"North", int64(10)}}}}
	modelClient := &fakeModelClient{}
	audit := &fakeAudit{}

	executor := NewExecutor(
		runs,
		steps,
		&fakeDatasetStore{},
		&fakeColumnStore{},
		&fakeObjectStore{},
		&fakeProvider{handle: handle},
		audit,
		modelClient,
		&fakeRelationshipLister{},
		&fakeMetricLister{},
		&fakeCorrectionLister{},
		discardLogger(),
	)
	return executor, runs, steps, handle, modelClient, audit
}

func testRun() domain.Run {
	return domain.Run{
		ID:           "run-1",
		WorkspaceID:  "ws-1",
		QuestionText: "How many customers per region?",
		Status:       domain.RunQueued,
		CreatedAt:    time.Now().UTC(),
	}
}

// TestExecutorHappyPath drives the full plan → generate_sql → execute pipeline and asserts every
// boundary the Phase 2 mandate names: run status transitions, step order and idempotency keys, input
// chaining (plan output → generate_sql input, generate_sql output → execute input), the engine view
// attachment under the canonical table name, JSON-scalar row conversion, and the query-executed audit
// entry.
func TestExecutorHappyPath(t *testing.T) {
	executor, runs, steps, handle, modelClient, audit := newTestExecutor()

	// A complete dataset whose view name the assertions below pin exactly.
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID:               "d5f3e2a1b9c8472d9a1b2c3d4e5f60718",
		WorkspaceID:      "ws-1",
		OriginalFilename: "Sales Data (Q3).csv",
		IngestStatus:     domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {
			{Name: "region", InferredType: domain.TypeString, NullRate: 0.02, DistinctCount: 12},
			{Name: "email", InferredType: domain.TypeString, PIIFlag: true, NullRate: 0.1, DistinctCount: 500},
			{Name: "amount", InferredType: domain.TypeNumber},
		},
	}}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"group by region"}`,
		`{"sql":"SELECT region, COUNT(*) AS cnt FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete (steps: %+v)", got, steps.all())
	}
	statuses := runs.allStatuses()
	wantStatuses := []domain.RunStatus{domain.RunWaking, domain.RunRunning, domain.RunComplete}
	if len(statuses) != len(wantStatuses) {
		t.Fatalf("status transitions = %v, want %v", statuses, wantStatuses)
	}
	for i := range wantStatuses {
		if statuses[i] != wantStatuses[i] {
			t.Errorf("status[%d] = %s, want %s", i, statuses[i], wantStatuses[i])
		}
	}

	all := steps.all()
	if len(all) != 3 {
		t.Fatalf("steps = %d, want 3", len(all))
	}
	wantNames := []domain.StepName{domain.StepPlan, domain.StepGenerateSQL, domain.StepExecute}
	for i, name := range wantNames {
		if all[i].StepName != name {
			t.Errorf("step[%d] name = %s, want %s", i, all[i].StepName, name)
		}
		if all[i].Status != domain.StepComplete {
			t.Errorf("step[%d] status = %s, want complete", i, all[i].Status)
		}
		if all[i].IdempotencyKey != "run-1:"+string(name) {
			t.Errorf("step[%d] idempotency key = %q, want %q", i, all[i].IdempotencyKey, "run-1:"+string(name))
		}
	}

	// Input chaining: the first model step gets no prior context, later steps get the previous
	// step's JSON-encoded output verbatim.
	if all[0].Input != "" {
		t.Errorf("plan input = %q, want empty", all[0].Input)
	}
	if all[1].Input != all[0].Output {
		t.Errorf("generate_sql input = %q, want the plan output %q", all[1].Input, all[0].Output)
	}
	if all[2].Input != all[1].Output {
		t.Errorf("execute input = %q, want the generate_sql output %q", all[2].Input, all[1].Output)
	}

	var plan domain.PlanStepOutput
	if err := json.Unmarshal([]byte(all[0].Output), &plan); err != nil {
		t.Fatalf("plan output not parseable: %v", err)
	}
	if !plan.Answerable || plan.Approach != "group by region" {
		t.Errorf("plan = %+v", plan)
	}

	var out domain.ExecuteStepOutput
	if err := json.Unmarshal([]byte(all[2].Output), &out); err != nil {
		t.Fatalf("execute output not parseable: %v", err)
	}
	if len(out.Columns) != 2 || out.Columns[0] != "region" || out.Columns[1] != "cnt" {
		t.Errorf("columns = %v", out.Columns)
	}
	if out.RowCount != 1 || len(out.Rows) != 1 {
		t.Fatalf("row_count = %d rows = %v", out.RowCount, out.Rows)
	}
	// After the JSON round trip, numbers arrive as float64 — the JSON scalar form ExecuteStepOutput
	// promises on the wire.
	if out.Rows[0][0] != "North" || out.Rows[0][1] != float64(10) {
		t.Errorf("row = %v, want [North 10] as JSON scalars", out.Rows[0])
	}

	// The engine view is attached under the exact canonical table name before the query runs.
	stmts := handle.statements()
	if len(stmts) != 1 {
		t.Fatalf("exec statements = %d, want 1: %v", len(stmts), stmts)
	}
	wantPrefix := "CREATE OR REPLACE VIEW dataset_sales_data_q3_csv_d5f3e2a1 AS SELECT * FROM read_parquet('"
	if !strings.HasPrefix(stmts[0], wantPrefix) || !strings.HasSuffix(stmts[0], ".parquet')") {
		t.Errorf("view statement = %q, want prefix %q and a .parquet temp path", stmts[0], wantPrefix)
	}

	// Temp file cleanup: the path embedded in the view statement must no longer exist.
	tempPath := strings.TrimSuffix(strings.TrimPrefix(stmts[0], wantPrefix), ".parquet')")
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Errorf("temp file %q still exists after the run, want cleanup", tempPath)
	}

	if got := modelClient.callCount(); got != 2 {
		t.Errorf("model calls = %d, want 2 (plan, generate_sql)", got)
	}
	if got := audit.queryCount(); got != 1 {
		t.Errorf("query-executed audit entries = %d, want 1", got)
	}
}

// TestExecutorNotAnswerableFailsRun pins the FEAT-033 Phase 2 boundary: an unanswerable question
// fails the run after the plan step — it never reaches generate_sql or the engine.
func TestExecutorNotAnswerableFailsRun(t *testing.T) {
	executor, runs, steps, _, modelClient, audit := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}

	modelClient.enqueue(`{"answerable":false,"reason":"The data has no column that records region."}`)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed", got)
	}
	all := steps.all()
	if len(all) != 1 {
		t.Fatalf("steps = %d, want 1 (plan only)", len(all))
	}
	if all[0].StepName != domain.StepPlan || all[0].Status != domain.StepComplete {
		t.Fatalf("step = %+v, want a COMPLETED plan step", all[0])
	}
	var plan domain.PlanStepOutput
	if err := json.Unmarshal([]byte(all[0].Output), &plan); err != nil {
		t.Fatalf("plan output not parseable: %v", err)
	}
	if plan.Answerable || plan.Reason != "The data has no column that records region." {
		t.Errorf("plan = %+v, want answerable=false with the model's reason", plan)
	}
	if got := modelClient.callCount(); got != 1 {
		t.Errorf("model calls = %d, want 1 (plan only)", got)
	}
	if got := audit.queryCount(); got != 0 {
		t.Errorf("query-executed audit entries = %d, want 0", got)
	}
}

func TestExecutorFailsWhenEngineUnavailable(t *testing.T) {
	executor, runs, steps, _, modelClient, _ := newTestExecutor()
	executor.provider = &fakeProvider{acquireErr: engine.ErrEngineUnavailable}

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed", got)
	}
	if all := steps.all(); len(all) != 0 {
		t.Errorf("steps = %v, want none — the engine never became available", all)
	}
	if got := modelClient.callCount(); got != 0 {
		t.Errorf("model calls = %d, want 0", got)
	}
}

func TestExecutorFailsWhenPlanStepReturnsGarbage(t *testing.T) {
	executor, runs, steps, _, modelClient, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	modelClient.enqueue("definitely not json")

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed", got)
	}
	all := steps.all()
	if len(all) != 1 || all[0].Status != domain.StepFailed {
		t.Fatalf("steps = %+v, want one FAILED plan step", all)
	}
	if !strings.Contains(all[0].Output, "unparseable") {
		t.Errorf("failed step output = %q, want it to name the parse failure", all[0].Output)
	}
}

func TestExecutorFailsWhenWorkspaceHasNoCompleteDatasets(t *testing.T) {
	executor, runs, steps, _, modelClient, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "abc", OriginalFilename: "still-ingesting.csv", IngestStatus: domain.IngestPending,
	}}}

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed", got)
	}
	all := steps.all()
	if len(all) != 1 || all[0].Status != domain.StepFailed {
		t.Fatalf("steps = %+v, want one FAILED plan step", all)
	}
	if !strings.Contains(all[0].Output, "no complete datasets") {
		t.Errorf("failed step output = %q, want it to name the missing complete datasets", all[0].Output)
	}
	if got := modelClient.callCount(); got != 0 {
		t.Errorf("model calls = %d, want 0 — no payload, no call", got)
	}
}

// TestTableNameForDataset pins the exact view-naming convention the model is taught
// (model.TableMetadata.Name) and prepareViews attaches: dataset_<stem>_<id8>.
func TestTableNameForDataset(t *testing.T) {
	tests := []struct {
		name string
		ds   domain.Dataset
		want string
	}{
		{"clean filename", domain.Dataset{ID: "abc123def456", OriginalFilename: "sales.csv"}, "dataset_sales_csv_abc123de"},
		{"spaces and punctuation collapse", domain.Dataset{ID: "abc123def456", OriginalFilename: "Sales Data (Q3).csv"}, "dataset_sales_data_q3_csv_abc123de"},
		{"long filename truncates to 40", domain.Dataset{ID: "abc123def456", OriginalFilename: strings.Repeat("x", 50) + ".csv"}, "dataset_" + strings.Repeat("x", 40) + "_abc123de"},
		{"non-ascii filename collapses to its extension", domain.Dataset{ID: "abc123def456", OriginalFilename: "订单数据.csv"}, "dataset_csv_abc123de"},
		{"fully non-ascii filename falls back", domain.Dataset{ID: "abc123def456", OriginalFilename: "订单数据"}, "dataset_dataset_abc123de"},
		{"id with no alphanumerics falls back", domain.Dataset{ID: "---", OriginalFilename: "data.csv"}, "dataset_data_csv_data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tableNameForDataset(tt.ds); got != tt.want {
				t.Errorf("tableNameForDataset() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeIdentifier(t *testing.T) {
	tests := []struct{ in, want string }{
		{"sales", "sales"},
		{"Sales Data", "sales_data"},
		{"order.line", "order_line"},
		{"__leading__trailing__", "leading__trailing"},
		{"a  b", "a_b"},
		{"!!!", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := sanitizeIdentifier(tt.in); got != tt.want {
			t.Errorf("sanitizeIdentifier(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConvertValue(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		in   any
		want any
	}{
		{nil, nil},
		{"text", "text"},
		{float64(1.5), float64(1.5)},
		{true, true},
		{int(7), int64(7)},
		{int32(7), int64(7)},
		{int64(7), int64(7)},
		{float32(1.5), float64(1.5)},
		{now, "2026-08-06T12:30:00Z"},
		{[]byte("bytes"), "bytes"},
		{struct{}{}, "{}"},
	}
	for _, tt := range tests {
		if got := convertValue(tt.in); got != tt.want {
			t.Errorf("convertValue(%#v) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}
