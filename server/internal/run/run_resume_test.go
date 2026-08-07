package run

import (
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

func completedPlanStep() domain.Step {
	now := time.Now().UTC()
	return domain.Step{
		ID:             "plan-step",
		RunID:          "run-1",
		StepName:       domain.StepPlan,
		Status:         domain.StepComplete,
		Output:         `{"answerable":true,"reason":"","approach":"group by region"}`,
		IdempotencyKey: "run-1:plan",
		StartedAt:      &now,
	}
}

// TestResumeReplaysCompletedStepsAndRunsFromFirstUnfinished drives FEAT-022's resume path: a run
// interrupted mid-pipeline (plan completed, generate_sql never started, run marked failed) is
// resumed, and the step machine replays the completed plan WITHOUT re-calling the model, while
// executing every unfinished step fresh. The idempotency + skip guarantees mean the caller cannot
// double-execute a step.
func TestResumeReplaysCompletedStepsAndRunsFromFirstUnfinished(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	seedDatasetExecutor(executor)

	// The run was interrupted after plan; its plan step is already persisted complete.
	steps.steps = []domain.Step{completedPlanStep()}

	failedRun := testRun()
	failedRun.Status = domain.RunFailed
	runs.run = failedRun

	// Plan is replayed (no model call), so only generate_sql, chart_spec, and narrate call the model.
	modelClient.enqueue(
		`{"sql":"SELECT region, COUNT(*) AS cnt FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","series_column":"","title":"Customers by region"}`,
		`{"narrative":"North leads the count."}`,
	)

	executor.Resume("user-1", "run-1")
	waitFor(t, func() bool { return runs.current().Status == domain.RunComplete })

	// Plan was never re-run (replayed), so the model was called exactly 3 times, never for plan.
	if got := modelClient.callCount(); got != 3 {
		t.Fatalf("model calls = %d, want 3 (plan replayed, not re-called)", got)
	}

	all := steps.all()
	if len(all) != 6 {
		t.Fatalf("steps = %d, want 6 (plan + generate_sql + static_check + execute + chart_spec + narrate): %+v", len(all), all)
	}
	// The replayed plan keeps its original row id and stays complete.
	for _, s := range all {
		if s.StepName == domain.StepPlan {
			if s.ID != "plan-step" || s.Status != domain.StepComplete {
				t.Fatalf("plan replayed incorrectly: id=%s status=%s", s.ID, s.Status)
			}
		}
	}
	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete", got)
	}
}
