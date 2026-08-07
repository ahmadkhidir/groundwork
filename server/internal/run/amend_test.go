package run

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// amendedRunSteps builds the persisted steps of a halted run whose generate_sql is about to be
// amended: every step completed once, so the test can prove amend re-runs generate_sql and everything
// downstream while leaving plan untouched (FEAT-024 AC3).
func amendedRunSteps() []domain.Step {
	now := time.Now().UTC()
	return []domain.Step{
		{ID: "plan-step", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete,
			Output: `{"answerable":true,"reason":"","approach":"group by region"}`, IdempotencyKey: "run-1:plan",
			StartedAt: &now, CompletedAt: &now},
		{ID: "gen-step", RunID: "run-1", StepName: domain.StepGenerateSQL, Status: domain.StepComplete,
			Output:         `{"sql":"SELECT region, COUNT(*) AS cnt FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"}`,
			IdempotencyKey: "run-1:generate_sql", StartedAt: &now, CompletedAt: &now},
		{ID: "sc-step", RunID: "run-1", StepName: domain.StepStaticCheck, Status: domain.StepComplete,
			Output: `{"warnings":null}`, IdempotencyKey: "run-1:static_check", StartedAt: &now, CompletedAt: &now},
		{ID: "exec-step", RunID: "run-1", StepName: domain.StepExecute, Status: domain.StepComplete,
			Output: `{"columns":["region","cnt"],"rows":[["North",10]],"row_count":1}`, IdempotencyKey: "run-1:execute",
			StartedAt: &now, CompletedAt: &now},
		{ID: "chart-step", RunID: "run-1", StepName: domain.StepChartSpec, Status: domain.StepComplete,
			Output:         `{"chart_spec":{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Customers by region"}}`,
			IdempotencyKey: "run-1:chart_spec", StartedAt: &now, CompletedAt: &now},
		{ID: "narrate-step", RunID: "run-1", StepName: domain.StepNarrate, Status: domain.StepComplete,
			Output: `{"narrative":"North leads the count."}`, IdempotencyKey: "run-1:narrate",
			StartedAt: &now, CompletedAt: &now},
	}
}

// TestAmendAndResumeReRunsFromAmendedStep drives FEAT-024's amend: a halted run whose generate_sql
// is amended re-seeds that step with the edited SQL and re-executes everything downstream, while the
// completed plan step is preserved untouched (AC3). Existing step rows are re-claimed — no new rows,
// so the idempotency_key UNIQUE constraint is never violated.
func TestAmendAndResumeReRunsFromAmendedStep(t *testing.T) {
	executor, runs, steps, _, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID:               "d5f3e2a1b9c8472d9a1b2c3d4e5f60718",
		WorkspaceID:      "ws-1",
		OriginalFilename: "Sales Data (Q3).csv",
		IngestStatus:     domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {
			{Name: "region", InferredType: domain.TypeString},
			{Name: "amount", InferredType: domain.TypeNumber},
		},
	}}

	haltedRun := testRun()
	haltedRun.Status = domain.RunHalted
	runs.run = haltedRun
	steps.steps = amendedRunSteps()

	// After amend, only chart_spec and narrate call the model: generate_sql is seeded verbatim and
	// plan is never re-asked (its row replays untouched).
	modelClient.enqueue(
		`{"chart_type":"bar","x_axis_column":"region","y_axis_column":"total","title":"Sales by region"}`,
		`{"narrative":"North leads sales."}`,
	)

	edited := "SELECT region, SUM(amount) AS total FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"
	executor.AmendAndResume("user-1", "run-1", domain.StepGenerateSQL, edited)
	waitFor(t, func() bool { return runs.current().Status == domain.RunComplete })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete after amend", got)
	}

	all := steps.all()
	byName := make(map[domain.StepName]domain.Step, len(all))
	for _, s := range all {
		byName[s.StepName] = s
	}

	// Plan preserved untouched: the original row, still complete, original output.
	if p := byName[domain.StepPlan]; p.ID != "plan-step" || p.Status != domain.StepComplete ||
		p.Output != `{"answerable":true,"reason":"","approach":"group by region"}` {
		t.Errorf("plan = %+v, want the original plan-step untouched", p)
	}

	// generate_sql re-ran in its original row, seeded with the amended SQL.
	gen := byName[domain.StepGenerateSQL]
	if gen.ID != "gen-step" || gen.Status != domain.StepComplete {
		t.Fatalf("generate_sql = %+v, want the original row re-run to complete", gen)
	}
	var genOut domain.GenerateSQLStepOutput
	if err := json.Unmarshal([]byte(gen.Output), &genOut); err != nil {
		t.Fatalf("generate_sql output: %v", err)
	}
	if genOut.SQL != edited {
		t.Errorf("generate_sql SQL = %q, want the amended input %q", genOut.SQL, edited)
	}

	// Downstream all re-ran to complete in their original rows.
	for _, name := range []domain.StepName{domain.StepStaticCheck, domain.StepExecute, domain.StepChartSpec, domain.StepNarrate} {
		s := byName[name]
		if s.Status != domain.StepComplete {
			t.Errorf("%s = %+v, want complete after amend", name, s)
		}
	}
	if len(all) != 6 {
		t.Errorf("steps = %d, want 6 — amend re-uses rows, it never inserts new ones", len(all))
	}

	// Exactly two model calls (chart_spec, narrate); plan and generate_sql were not re-asked.
	if got := modelClient.callCount(); got != 2 {
		t.Errorf("model calls = %d, want 2 (chart_spec, narrate)", got)
	}

	// A report still materializes for the amended run.
	if created := reports.created(); len(created) != 1 {
		t.Errorf("created reports = %d, want 1", len(created))
	}
}

// TestAmendRejectsEmptySQL pins the amend guard: an empty amended input is refused before any row is
// invalidated, so a mistaken blank edit never clobbers a halted run's state.
func TestAmendRejectsEmptySQL(t *testing.T) {
	executor, runs, steps, _, _, _, _ := newTestExecutor()
	haltedRun := testRun()
	haltedRun.Status = domain.RunHalted
	runs.run = haltedRun
	steps.steps = amendedRunSteps()

	executor.AmendAndResume("user-1", "run-1", domain.StepGenerateSQL, "   ")
	time.Sleep(50 * time.Millisecond)

	// Nothing was invalidated and nothing re-ran: every step is still exactly as seeded.
	all := steps.all()
	if len(all) != 6 {
		t.Fatalf("steps = %d, want the 6 seeded rows untouched", len(all))
	}
	for _, s := range all {
		if s.Status != domain.StepComplete {
			t.Errorf("step %s = %s, want still complete after an empty amend", s.StepName, s.Status)
		}
	}
	if got := runs.current().Status; got != domain.RunHalted {
		t.Errorf("run status = %s, want still halted after an empty amend", got)
	}
}
