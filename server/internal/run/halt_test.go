package run

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
)

// blockingModelClient parks on the run's context once a call begins, so a run can be held mid-step
// (parked on plan) while a halt interrupts it. It honours ctx cancellation like a real model client:
// on cancel it returns context.Canceled, which the executor's stepFailed path reads as a halt.
type blockingModelClient struct {
	mu    sync.Mutex
	calls int
}

func (f *blockingModelClient) Complete(ctx context.Context, _, _, _ string, _ model.Payload) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	<-ctx.Done()
	return "", context.Canceled
}

func (f *blockingModelClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestExecutorHaltSettlesInFlightRunToHalted drives FEAT-024's halt: a run parked mid-step (its plan
// call blocked on the run context) is halted, the run reads halted, and the in-flight step row is
// settled to halted — a halt is never reported as a failure.
func TestExecutorHaltSettlesInFlightRunToHalted(t *testing.T) {
	executor, runs, steps, _, _, _, _ := newTestExecutor()
	executor.client = &blockingModelClient{}
	seedDatasetExecutor(executor)

	// Seed the store with the run BEFORE Submit: Halt reloads it from the store, and needs its ID +
	// workspace to locate and cancel the right execution context.
	runs.run = testRun()
	executor.Submit("user-1", testRun())
	// Wait for the run to actually start and park on the blocking plan call.
	waitFor(t, func() bool {
		return executor.client.(*blockingModelClient).callCount() > 0
	})

	executor.Halt("user-1", "run-1")
	waitFor(t, func() bool { return runs.current().Status == domain.RunHalted })
	// The run reads halted as soon as the Halt handler reflects it, but the in-flight step settles
	// when the engine goroutine's cancelled work unwinds — wait for that too before asserting rows.
	waitFor(t, func() bool {
		for _, st := range steps.all() {
			if st.StepName == domain.StepPlan && st.Status == domain.StepHalted {
				return true
			}
		}
		return false
	})

	if got := runs.current().Status; got != domain.RunHalted {
		t.Fatalf("run status = %s, want halted (transitions: %v)", got, runs.allStatuses())
	}
	// The only step that existed (plan, parked on the model call) is settled to halted, not failed.
	all := steps.all()
	if len(all) != 1 {
		t.Fatalf("steps = %+v, want exactly the one in-flight plan step", all)
	}
	if all[0].StepName != domain.StepPlan || all[0].Status != domain.StepHalted {
		t.Fatalf("step[0] = %s/%s, want plan/halted", all[0].StepName, all[0].Status)
	}
}

// TestHaltOnCompleteRunIsNoOp pins the halt guard: halting a run that already reached a terminal
// state leaves the run untouched. The handler remains idempotent — no error surfaces to the user.
func TestHaltOnCompleteRunIsNoOp(t *testing.T) {
	executor, runs, steps, _, modelClient, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID:               "d5f3e2a1b9c8472d9a1b2c3d4e5f60718",
		WorkspaceID:      "ws-1",
		OriginalFilename: "Sales Data (Q3).csv",
		IngestStatus:     domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {
			{Name: "region", InferredType: domain.TypeString},
		},
	}}
	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_data_q3_csv_d5f3e2a1"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions are listed."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status == domain.RunComplete })

	executor.Halt("user-1", "run-1")
	time.Sleep(100 * time.Millisecond)

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want still complete after halting a complete run", got)
	}
	for _, st := range steps.all() {
		if st.Status != domain.StepComplete {
			t.Errorf("step %s = %s, want untouched complete after no-op halt", st.StepName, st.Status)
		}
	}
}
