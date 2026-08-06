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

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// singleColumnRows builds a fake result set of n rows, each a single "region" value, for driving
// row-count sensitive paths without allocating an outsize slice by hand.
func singleColumnRows(n int) [][]any {
	out := make([][]any, n)
	for i := range out {
		out[i] = []any{"region-val"}
	}
	return out
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
	queried        []string
	rows           engine.Rows
	// countRows, when set, is returned for SELECT count(*) queries (reconciliation measurement),
	// so the fake can produce a distinct row count without exhausting rows. Kept as data, not an
	// engine.Rows, so each count query gets a fresh cursor (fakeRows is stateful).
	countRows [][]any
}

func (f *fakeHandle) WorkspaceID() string { return "ws-1" }
func (f *fakeHandle) AcquiredAt() time.Time {
	return time.Now()
}

func (f *fakeHandle) Query(_ context.Context, sql string, _ ...any) (engine.Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried = append(f.queried, sql)
	if f.countRows != nil && strings.HasPrefix(sql, "SELECT count(*)") {
		return &fakeRows{cols: []string{"count"}, rows: f.countRows}, nil
	}
	return f.rows, nil
}

func (f *fakeHandle) queriedSQL() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.queried))
	copy(out, f.queried)
	return out
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

type fakeReportStore struct {
	mu             sync.Mutex
	createdReports []domain.Report
	err            error
}

func (f *fakeReportStore) Create(_ context.Context, report domain.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.createdReports = append(f.createdReports, report)
	return nil
}

func (f *fakeReportStore) created() []domain.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Report, len(f.createdReports))
	copy(out, f.createdReports)
	return out
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
func newTestExecutor() (*Executor, *fakeRunStore, *fakeStepStore, *fakeHandle, *fakeModelClient, *fakeAudit, *fakeReportStore) {
	runs := &fakeRunStore{}
	steps := &fakeStepStore{}
	handle := &fakeHandle{rows: &fakeRows{cols: []string{"region", "cnt"}, rows: [][]any{{"North", int64(10)}}}}
	modelClient := &fakeModelClient{}
	audit := &fakeAudit{}
	reports := &fakeReportStore{}

	executor := NewExecutor(
		runs,
		steps,
		&fakeDatasetStore{},
		&fakeColumnStore{},
		&fakeObjectStore{},
		&fakeProvider{handle: handle},
		audit,
		modelClient,
		reports,
		&fakeRelationshipLister{},
		&fakeMetricLister{},
		&fakeCorrectionLister{},
		discardLogger(),
	)
	return executor, runs, steps, handle, modelClient, audit, reports
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
	executor, runs, steps, handle, modelClient, audit, reports := newTestExecutor()

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
		`{"chart_type":"bar","x_axis_column":"region","y_axis_column":"cnt","title":"Customers by region"}`,
		`{"narrative":"The dataset has sales across regions; North leads the count."}`,
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
	if len(all) != 5 {
		t.Fatalf("steps = %d, want 5", len(all))
	}
	wantNames := []domain.StepName{domain.StepPlan, domain.StepGenerateSQL, domain.StepExecute, domain.StepChartSpec, domain.StepNarrate}
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
	// step's JSON-encoded output verbatim — chart_spec and narrate both consume the execute output.
	if all[0].Input != "" {
		t.Errorf("plan input = %q, want empty", all[0].Input)
	}
	if all[1].Input != all[0].Output {
		t.Errorf("generate_sql input = %q, want the plan output %q", all[1].Input, all[0].Output)
	}
	if all[2].Input != all[1].Output {
		t.Errorf("execute input = %q, want the generate_sql output %q", all[2].Input, all[1].Output)
	}
	if all[3].Input != all[2].Output {
		t.Errorf("chart_spec input = %q, want the execute output %q", all[3].Input, all[2].Output)
	}
	if all[4].Input != all[2].Output {
		t.Errorf("narrate input = %q, want the execute output %q", all[4].Input, all[2].Output)
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

	// chart_spec (FEAT-016) output must parse as a ChartSpec.
	var chartOut domain.ChartSpecStepOutput
	if err := json.Unmarshal([]byte(all[3].Output), &chartOut); err != nil {
		t.Fatalf("chart_spec output not parseable: %v", err)
	}
	if chartOut.ChartSpec.ChartType != domain.ChartBar || chartOut.ChartSpec.XAxisColumn != "region" || chartOut.ChartSpec.YAxisColumn != "cnt" {
		t.Errorf("chart_spec = %+v", chartOut.ChartSpec)
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

	// Four model calls: plan, generate_sql, chart_spec, narrate.
	if got := modelClient.callCount(); got != 4 {
		t.Errorf("model calls = %d, want 4 (plan, generate_sql, chart_spec, narrate)", got)
	}
	if got := audit.queryCount(); got != 1 {
		t.Errorf("query-executed audit entries = %d, want 1", got)
	}

	// The run materializes one fully self-contained Report.
	created := reports.created()
	if len(created) != 1 {
		t.Fatalf("created reports = %d, want 1", len(created))
	}
	rep := created[0]
	if rep.RunID != "run-1" || rep.WorkspaceID != "ws-1" {
		t.Errorf("report run/workspace = %s/%s, want run-1/ws-1", rep.RunID, rep.WorkspaceID)
	}
	if rep.Title != testRun().QuestionText {
		t.Errorf("report title = %q, want the question text", rep.Title)
	}
	// The report's narrative is exactly the narrate step's output.
	var narrateOut domain.NarrateStepOutput
	if err := json.Unmarshal([]byte(all[4].Output), &narrateOut); err != nil {
		t.Fatalf("narrate output not parseable: %v", err)
	}
	if rep.Narrative != narrateOut.Narrative {
		t.Errorf("report narrative = %q, want the narrate output %q", rep.Narrative, narrateOut.Narrative)
	}
	if rep.ChartSpec.ChartType != domain.ChartBar {
		t.Errorf("report chart_spec = %+v, want the chart_spec output", rep.ChartSpec)
	}
	// rows_out is the executed row count; provenance_links is always an empty array this wave.
	if rep.ReconciliationSummary.RowsOut != 1 {
		t.Errorf("report rows_out = %d, want 1", rep.ReconciliationSummary.RowsOut)
	}
	if rep.ProvenanceLinks == nil || len(rep.ProvenanceLinks) != 0 {
		t.Errorf("report provenance_links = %v, want an empty array", rep.ProvenanceLinks)
	}
}

// TestExecutorSeededGenerateSQL pins FEAT-018's seeded path: SubmitWithSQL skips plan and the model
// generate_sql call, persists the user's edited SQL verbatim as the generate_sql step output, then
// continues the ordinary execute → chart_spec → narrate → report tail. The engine runs the seeded
// SQL exactly, and the run completes with one materialized report.
func TestExecutorSeededSubmit(t *testing.T) {
	executor, runs, steps, _, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "Sales Data (Q3).csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {
			{Name: "region", InferredType: domain.TypeString},
			{Name: "amount", InferredType: domain.TypeNumber},
		},
	}}
	handle := &fakeHandle{
		rows: &fakeRows{cols: []string{"region", "total"}, rows: [][]any{{"North", float64(100)}}},
		// Reconciliation measurement needs a distinct count result; distinct from the main rows so
		// the count(*) query drives a fresh cursor rather than exhausting the shared one.
		countRows: [][]any{{int64(3)}},
	}
	executor.provider = &fakeProvider{handle: handle}

	// Only chart_spec and narrate consume the model; plan and generate_sql are replaced by the seed.
	modelClient.enqueue(
		`{"chart_type":"bar","x_axis_column":"region","y_axis_column":"total","title":"Sales by region"}`,
		`{"narrative":"North leads sales."}`,
	)

	edited := "SELECT region, SUM(amount) AS total FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"
	executor.SubmitWithSQL("user-1", testRun(), "  "+edited+"  ")
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete (steps: %+v)", got, steps.all())
	}

	all := steps.all()
	if len(all) != 4 {
		t.Fatalf("steps = %d, want 4 (generate_sql, execute, chart_spec, narrate)", len(all))
	}
	wantNames := []domain.StepName{domain.StepGenerateSQL, domain.StepExecute, domain.StepChartSpec, domain.StepNarrate}
	for i, name := range wantNames {
		if all[i].StepName != name || all[i].Status != domain.StepComplete {
			t.Errorf("step[%d] = %s/%s, want %s complete", i, all[i].StepName, all[i].Status, name)
		}
	}

	// The generate_sql step has no chained plan input and its output is the user's trimmed SQL.
	if all[0].Input != "" {
		t.Errorf("generate_sql input = %q, want empty — no plan step precedes it", all[0].Input)
	}
	var gen domain.GenerateSQLStepOutput
	if err := json.Unmarshal([]byte(all[0].Output), &gen); err != nil {
		t.Fatalf("generate_sql output not parseable: %v", err)
	}
	if gen.SQL != edited {
		t.Errorf("generate_sql SQL = %q, want the user's trimmed SQL verbatim %q", gen.SQL, edited)
	}

	// The engine ran the seeded SQL exactly (queried includes the execute step's query).
	queried := handle.queriedSQL()
	found := false
	for _, q := range queried {
		if q == edited {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("engine queries = %v, want the seeded SQL %q among them", queried, edited)
	}

	// Two model calls only: chart_spec and narrate.
	if got := modelClient.callCount(); got != 2 {
		t.Errorf("model calls = %d, want 2 (chart_spec, narrate)", got)
	}

	// A report still materializes for the seeded run.
	if created := reports.created(); len(created) != 1 {
		t.Errorf("created reports = %d, want 1", len(created))
	}
}

// TestExecutorSeedRejectsEmptySQL pins the executor's guard: SubmitWithSQL with a whitespace-only
// sql must fail the run at the generate_sql step — a run never executes nothing.
func TestExecutorSeedRejectsEmptySQL(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {
			{Name: "region", InferredType: domain.TypeString},
		},
	}}

	executor.SubmitWithSQL("user-1", testRun(), "   ")
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed for empty edited SQL", got)
	}
	all := steps.all()
	if len(all) != 1 || all[0].StepName != domain.StepGenerateSQL || all[0].Status != domain.StepFailed {
		t.Fatalf("steps = %+v, want one FAILED generate_sql step", all)
	}
	if !strings.Contains(all[0].Output, "empty") {
		t.Errorf("failed generate_sql output = %q, want it to name the empty SQL", all[0].Output)
	}
	if got := modelClient.callCount(); got != 0 {
		t.Errorf("model calls = %d, want 0", got)
	}
}

// TestExecutorNotAnswerableFailsRun pins the FEAT-033 Phase 2 boundary: an unanswerable question
// fails the run after the plan step — it never reaches generate_sql or the engine.
func TestExecutorNotAnswerableFailsRun(t *testing.T) {
	executor, runs, steps, _, modelClient, audit, _ := newTestExecutor()
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
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
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
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
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
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
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

// TestExecutorFailsWhenResultExceedsRowCeiling pins FEAT-014's Honest Output Cap: a query that
// returns more rows than config.MaxExecuteRows fails the execute step (and the run) with an honest
// message — never a silent truncation of the result set.
func TestExecutorFailsWhenResultExceedsRowCeiling(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.provider = &fakeProvider{handle: &fakeHandle{rows: &fakeRows{
		cols: []string{"region"},
		// One row over the ceiling. Executor builds the full result set before checking, so a few
		// rows above the cap is enough to fail without allocating an outsize slice.
		rows: singleColumnRows(config.MaxExecuteRows + 1),
	}}}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed after the row-count ceiling is exceeded", got)
	}
	all := steps.all()
	if len(all) != 3 {
		t.Fatalf("steps = %d, want 3 (plan, generate_sql, execute)", len(all))
	}
	if all[2].Status != domain.StepFailed {
		t.Fatalf("execute step status = %s, want failed", all[2].Status)
	}
	if !strings.Contains(all[2].Output, "more than") {
		t.Errorf("failed execute output = %q, want it to name the row ceiling honestly", all[2].Output)
	}
	if got := modelClient.callCount(); got != 2 {
		t.Errorf("model calls = %d, want 2 (plan, generate_sql) — execute never reaches chart/narrate", got)
	}
}

// TestExecutorFailsWhenChartSpecStepReturnsGarbage pins FEAT-016's parse boundary: an unparseable
// chart_spec response fails the run at the chart_spec step, before narrate or report materialization.
func TestExecutorFailsWhenChartSpecStepReturnsGarbage(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"group by region"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		"definitely not json",
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed after unparseable chart_spec", got)
	}
	all := steps.all()
	if len(all) != 4 {
		t.Fatalf("steps = %d, want 4 (plan, generate_sql, execute, chart_spec)", len(all))
	}
	if all[3].Status != domain.StepFailed {
		t.Fatalf("chart_spec step status = %s, want failed", all[3].Status)
	}
	if !strings.Contains(all[3].Output, "unparseable") {
		t.Errorf("failed chart_spec output = %q, want it to name the parse failure", all[3].Output)
	}
}

// TestExecutorFailsWhenNarrateStepReturnsGarbage pins FEAT-015's parse boundary: an unparseable
// narrate response fails the run at the narrate step — never reaching report materialization.
func TestExecutorFailsWhenNarrateStepReturnsGarbage(t *testing.T) {
	executor, runs, steps, _, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"group by region"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		"definitely not json",
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed after unparseable narrate", got)
	}
	all := steps.all()
	if len(all) != 5 {
		t.Fatalf("steps = %d, want 5 (plan, generate_sql, execute, chart_spec, narrate)", len(all))
	}
	if all[4].Status != domain.StepFailed {
		t.Fatalf("narrate step status = %s, want failed", all[4].Status)
	}
	if !strings.Contains(all[4].Output, "unparseable") {
		t.Errorf("failed narrate output = %q, want it to name the parse failure", all[4].Output)
	}
	if created := reports.created(); len(created) != 0 {
		t.Errorf("created reports = %v, want none — a failed narrate must not materialize a report", created)
	}
}

// TestExecutorFailsWhenReportPersistenceFails pins FEAT-014's completion boundary: a run whose steps
// all succeeded but whose Report cannot be persisted fails the run — a corrupt state is never
// reported as complete.
func TestExecutorFailsWhenReportPersistenceFails(t *testing.T) {
	executor, runs, steps, _, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	reports.err = errors.New("insert failed")
	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"group by region"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions are listed."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed when report persistence fails", got)
	}
	all := steps.all()
	if len(all) != 5 {
		t.Fatalf("steps = %d, want 5 — chart_spec and narrate still completed", len(all))
	}
	for i, step := range all {
		if step.Status != domain.StepComplete {
			t.Errorf("step[%d] status = %s, want complete — failure is after the steps", i, step.Status)
		}
	}
}

// TestMeasureRowsIn pins the reconciliation rows_in measurement: the SQL text is scanned for each
// known dataset view name, and the count(*) per referenced view is summed. SQL referencing no known
// view records an explicit exclusion warning with rows_in 0 rather than a fabricated number.
func TestMeasureRowsIn(t *testing.T) {
	executor, _, _, _, _, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{
		{ID: "aaaaaaaaaaaaaaaa", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete},
		{ID: "bbbbbbbbbbbbbbbb", OriginalFilename: "orders.csv", IngestStatus: domain.IngestComplete},
		{ID: "cccccccccccccccc", OriginalFilename: "still-ingesting.csv", IngestStatus: domain.IngestPending},
	}}
	handle := &fakeHandle{countRows: [][]any{{int64(500)}}}

	// SQL joining both complete datasets references both views.
	rowsIn, warnings, err := executor.measureRowsIn(context.Background(), handle,
		`SELECT c.name FROM dataset_customers_csv_aaaaaaaa c JOIN dataset_orders_csv_bbbbbbbb o ON o.customer_id = c.id`,
		"ws-1")
	if err != nil {
		t.Fatalf("measureRowsIn: %v", err)
	}
	if rowsIn != 1000 {
		t.Errorf("rows_in = %d, want 1000 (500 per referenced view)", rowsIn)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none when views are referenced", warnings)
	}

	// SQL referencing no known dataset view gets rows_in 0 plus an explicit exclusion warning.
	rowsIn, warnings, err = executor.measureRowsIn(context.Background(), handle,
		`SELECT 1 FROM information_schema.tables`, "ws-1")
	if err != nil {
		t.Fatalf("measureRowsIn (no view): %v", err)
	}
	if rowsIn != 0 {
		t.Errorf("rows_in = %d, want 0 for unmatched SQL", rowsIn)
	}
	if len(warnings) == 0 {
		t.Errorf("warnings = %v, want an explicit exclusion warning for unmatched SQL", warnings)
	}
}

// TestDeriveReportTitle pins the title derivation: whitespace collapses and plain truncation to 80
// characters, exactly as schema.md's "short, human-readable title" is interpreted.
func TestDeriveReportTitle(t *testing.T) {
	if got := deriveReportTitle("How many customers per region?"); got != "How many customers per region?" {
		t.Errorf("deriveReportTitle(short) = %q", got)
	}
	if got := deriveReportTitle("   What   are   monthly   sales?  "); got != "What are monthly sales?" {
		t.Errorf("deriveReportTitle(collapse) = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := deriveReportTitle(long); got != strings.Repeat("x", 80) {
		t.Errorf("deriveReportTitle(long) length = %d, want 80", len(got))
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
