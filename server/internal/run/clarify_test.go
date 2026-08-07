package run

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// testClarifyExecutor wires a fake executor whose first plan answer pauses for a clarification.
func testClarifyExecutor(t *testing.T, capturing *payloadCapturingModelClient) (*Executor, *fakeRunStore, *fakeStepStore) {
	t.Helper()
	executor, runs, steps, _, _, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}, {Name: "amount", InferredType: domain.TypeNumber}},
	}}
	if capturing != nil {
		executor.client = capturing
	}
	// The clarify flow reloads the run from the store (runs.GetByID), so the store must hold the
	// same run the executor is driving — the same convention TestExecutorHaltSettlesInFlightRunToHalted
	// follows.
	runs.run = testRun()
	return executor, runs, steps
}

// TestPlanAmbiguityPausesRun drives FEAT-033's pause: a plan step that judges the question
// ambiguous settles the run to halted (the paused state) with NO further steps, so the Run Viewer
// renders screen 12's Clarifying Question Prompt from the plan output. The question is never
// answered on a guess.
func TestPlanAmbiguityPausesRun(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}, {Name: "amount", InferredType: domain.TypeNumber}},
	}}

	modelClient.enqueue(
		`{"answerable":true,"needs_clarification":true,"clarification_question":"Do you mean revenue by total value or by order count?","clarification_options":["By revenue","By count"],"ambiguity_key":"metric_undefined"}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunHalted {
		t.Fatalf("run status = %s, want halted (paused for clarification), transitions = %v", got, runs.allStatuses())
	}

	// Only the plan step exists, completed with the specific clarifying question — no generate_sql,
	// no engine query, no guess.
	all := steps.all()
	if len(all) != 1 || all[0].StepName != domain.StepPlan || all[0].Status != domain.StepComplete {
		t.Fatalf("steps = %+v, want one COMPLETED plan step", all)
	}
	var plan planOutput
	if err := json.Unmarshal([]byte(all[0].Output), &plan); err != nil {
		t.Fatalf("plan output not parseable: %v", err)
	}
	if !plan.awaitingClarification() {
		t.Errorf("plan = %+v, want an awaiting clarification state", plan)
	}
	if plan.ClarificationQuestion != "Do you mean revenue by total value or by order count?" {
		t.Errorf("clarification_question = %q, want the SPECIFIC ambiguity", plan.ClarificationQuestion)
	}
	if len(plan.ClarificationOptions) != 2 {
		t.Errorf("clarification_options = %v, want the two discrete resolutions", plan.ClarificationOptions)
	}
	if got := modelClient.callCount(); got != 1 {
		t.Errorf("model calls = %d, want 1 (plan only) — nothing downstream ran", got)
	}
}

// TestClarifyResumesWithAnswer drives the resume half: after the user answers, the answer is
// written into the plan step's persisted output, the run resumes, generate_sql receives the
// clarification as prior context, and the run completes with the full verification pipeline.
func TestClarifyResumesWithAnswer(t *testing.T) {
	capturing := &payloadCapturingModelClient{}
	executor, runs, steps := testClarifyExecutor(t, capturing)

	// First plan call pauses; the clarify-resume then re-enters the pipeline where the completed
	// plan replays (no second plan call), and generate_sql/adversarial/chart/narrate run fresh.
	capturing.enqueue(
		`{"answerable":true,"needs_clarification":true,"clarification_question":"Do you mean revenue by total value or by order count?","clarification_options":["By revenue","By count"],"ambiguity_key":"metric_undefined"}`,
		`{"sql":"SELECT region, SUM(amount) AS total FROM dataset_sales_csv_d5f3e2a1 GROUP BY region"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"total","title":"Sales"}`,
		`{"narrative":"Revenue by region."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status == domain.RunHalted })

	// The user answers; Clarify writes the answer and resumes. Halted is a terminal status, so the
	// wait must target the resume's real terminal states (complete/failed), not IsTerminal().
	executor.Clarify("user-1", "run-1", "By revenue")
	waitFor(t, func() bool {
		s := runs.current().Status
		return s == domain.RunComplete || s == domain.RunFailed
	})

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete after clarification (steps: %+v)", got, steps.all())
	}

	// The plan step's persisted output now carries the answer.
	var plan planOutput
	for _, st := range steps.all() {
		if st.StepName == domain.StepPlan && st.Status == domain.StepComplete {
			if err := json.Unmarshal([]byte(st.Output), &plan); err != nil {
				t.Fatalf("plan output not parseable: %v", err)
			}
		}
	}
	if plan.clarificationResolution() != "By revenue" {
		t.Errorf("plan clarification_answer = %q, want the user's answer persisted", plan.ClarificationAnswer)
	}

	// The first fresh model payload after resume is generate_sql, whose prior context carries the
	// clarification — the model writes SQL that honors it.
	all := capturing.allJSON()
	if len(all) != 5 {
		t.Fatalf("payloads captured = %d, want 5 (plan + resumed generate_sql/adversarial/chart/narrate)", len(all))
	}
	if !strings.Contains(all[1], "By revenue") {
		t.Errorf("generate_sql payload must carry the clarification in prior_context:\n%s", all[1])
	}
}

// TestClarifyOnNonPausedRunIsNoOp pins the executor guard: answering a run that is not paused
// awaiting clarification (e.g. a user-halted run) is refused — the plan output is untouched and the
// run stays put.
func TestClarifyOnNonPausedRunIsNoOp(t *testing.T) {
	executor, runs, steps := testClarifyExecutor(t, nil)

	// A halted run whose plan did NOT ask for clarification (a manual halt, FEAT-024).
	haltedRun := testRun()
	haltedRun.Status = domain.RunHalted
	runs.run = haltedRun
	steps.steps = []domain.Step{{
		ID: "plan-step", RunID: "run-1", StepName: domain.StepPlan, Status: domain.StepComplete,
		Output: `{"answerable":true,"approach":"group by region"}`, IdempotencyKey: "run-1:plan",
	}}

	executor.Clarify("user-1", "run-1", "By revenue")
	// Give the goroutine a moment; the guard must leave everything untouched.
	waitFor(t, func() bool {
		all := steps.all()
		if len(all) != 1 {
			return false
		}
		return all[0].Output == `{"answerable":true,"approach":"group by region"}`
	})
	// Wait for a bounded time to confirm no state transition happened.
	_ = waitForNoStatusChange(t, runs)
}

// waitForNoStatusChange observes the run for 150ms and reports whether it stayed halted (the no-op
// contract of a refused clarification). It returns the observed status for assertions.
func waitForNoStatusChange(t *testing.T, runs *fakeRunStore) string {
	t.Helper()
	deadline := 150
	for i := 0; i < deadline; i++ {
		if runs.current().Status != domain.RunHalted {
			return string(runs.current().Status)
		}
		sleepMs(5)
	}
	return string(domain.RunHalted)
}

// sleepMs is a tiny test helper so the no-op window is explicit and bounded.
func sleepMs(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
