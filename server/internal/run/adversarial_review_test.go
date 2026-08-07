package run

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
)

// payloadCapturingModelClient records every payload it was handed, so a test can assert FEAT-032's
// AC1 (the adversarial reviewer never sees results) on the WIRE shape — not just by construction.
type payloadCapturingModelClient struct {
	mu       sync.Mutex
	queue    []string
	payloads []model.Payload
}

func (f *payloadCapturingModelClient) enqueue(responses ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, responses...)
}

func (f *payloadCapturingModelClient) Complete(_ context.Context, _, _, _ string, payload model.Payload) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	if len(f.queue) == 0 {
		return "", nil
	}
	raw := f.queue[0]
	f.queue = f.queue[1:]
	return raw, nil
}

func (f *payloadCapturingModelClient) allJSON() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.payloads))
	for _, p := range f.payloads {
		j, err := p.JSON()
		if err != nil {
			out = append(out, "<error>")
			continue
		}
		out = append(out, j)
	}
	return out
}

// TestAdversarialReviewBlocksOnMismatch drives FEAT-032 AC2's blocking branch: the reviewer finds
// the SQL does not answer the question, the adversarial_review step FAILS with the verdict, and the
// run fails BEFORE chart_spec/narrate can show results — no report is materialized, and the SQL
// never reaches the facts-consuming steps.
func TestAdversarialReviewBlocksOnMismatch(t *testing.T) {
	executor, runs, steps, _, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}},
	}}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"matches":false,"verdict":"The question asks for active customers only, but the SQL has no status filter.","severity":"blocking"}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunFailed {
		t.Fatalf("run status = %s, want failed when the adversarial review blocks", got)
	}

	all := steps.all()
	if len(all) != 6 {
		t.Fatalf("steps = %d, want 6 (plan, generate_sql, static_check, execute, shadow_check, adversarial_review)", len(all))
	}
	adv := all[5]
	if adv.StepName != domain.StepAdversarialReview || adv.Status != domain.StepFailed {
		t.Fatalf("adversarial step = %s/%s, want adversarial_review failed", adv.StepName, adv.Status)
	}
	if !strings.Contains(adv.Output, "status filter") {
		t.Errorf("adversarial failure output = %q, want the verdict verbatim", adv.Output)
	}
	// The verdict is surfaced in the failed step's output, not buried.
	if got := modelClient.callCount(); got != 3 {
		t.Errorf("model calls = %d, want 3 — chart_spec/narrate never run after a block", got)
	}
	if created := reports.created(); len(created) != 0 {
		t.Errorf("created reports = %v, want none — results were never shown", created)
	}
}

// TestAdversarialReviewWarningContinues drives the severity carve-out: a warning-severity mismatch
// flags (the step completes with matches=false, severity=warning, verdict) but does NOT block the
// run — the viewer surfaces it while the answer still reaches the user.
func TestAdversarialReviewWarningContinues(t *testing.T) {
	executor, runs, steps, _, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}},
	}}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"matches":false,"verdict":"The question may imply a date filter the SQL omits.","severity":"warning"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions are listed."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete — a warning flags, it does not block", got)
	}
	var advOut AdversarialReviewStepOutput
	for _, st := range steps.all() {
		if st.StepName == domain.StepAdversarialReview && st.Status == domain.StepComplete {
			if err := json.Unmarshal([]byte(st.Output), &advOut); err != nil {
				t.Fatalf("adversarial output not parseable: %v", err)
			}
		}
	}
	if advOut.Matches || advOut.Severity != "warning" {
		t.Errorf("adversarial output = %+v, want matches=false severity=warning surfaced", advOut)
	}
	if len(reports.created()) != 1 {
		t.Errorf("reports = %d, want 1 — a warning still materializes the answer", len(reports.created()))
	}
}

// TestAdversarialReviewPayloadHasNoResults pins FEAT-032 AC1 at the wire: the reviewer's payload
// carries the question and the SQL (as prior_context) but NEVER a facts key — no result row can
// reach the second pass, even though the run already executed the query.
func TestAdversarialReviewPayloadHasNoResults(t *testing.T) {
	executor, runs, _, _, _, _, _ := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}},
	}}

	capturing := &payloadCapturingModelClient{}
	capturing.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions."}`,
	)
	executor.client = capturing

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete", got)
	}
	all := capturing.allJSON()
	if len(all) != 5 {
		t.Fatalf("payloads captured = %d, want 5", len(all))
	}
	// The third payload is adversarial_review (plan, generate_sql, adversarial, chart_spec, narrate).
	adv := all[2]
	if strings.Contains(adv, `"facts"`) {
		t.Errorf("adversarial_review payload carries a facts key — results reached the reviewer:\n%s", adv)
	}
	if !strings.Contains(adv, "SELECT region FROM dataset_sales_csv_d5f3e2a1") {
		t.Errorf("adversarial_review payload must carry the SQL in prior_context:\n%s", adv)
	}
}
