package run

import (
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// TestForkedRunReportLinksParent pins FEAT-021/FEAT-025's report-tree linkage: a run created with a
// parent_run_id (a fork-on-amendment or a compositional follow-up) materializes a report whose
// parent_report_id is the report the parent run produced — so report history reads as a traceable
// tree, and the original run/report is untouched.
func TestForkedRunReportLinksParent(t *testing.T) {
	executor, runs, steps, handle, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}},
	}}
	handle.sumRows = [][]any{{float64(0)}}
	handle.countRows = [][]any{{int64(0)}}

	// The parent run's report already exists (its run completed earlier).
	parentReport := domain.Report{
		ID: "report-parent", WorkspaceID: "ws-1", RunID: "run-parent",
		Title: "Parent answer", Narrative: "parent", CreatedAt: time.Now().UTC(),
	}
	reports.byRunID = map[string]domain.Report{"run-parent": parentReport}

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions."}`,
	)

	// The forked run records its parent BEFORE the executor sees it (the API handler does this).
	forked := testRun()
	forked.ParentRunID = "run-parent"
	runs.run = forked

	executor.Submit("user-1", forked)
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete (steps: %+v)", got, steps.all())
	}

	created := reports.created()
	if len(created) != 1 {
		t.Fatalf("created reports = %d, want 1", len(created))
	}
	if created[0].ParentReportID != "report-parent" {
		t.Errorf("forked report parent_report_id = %q, want report-parent — the tree must link back", created[0].ParentReportID)
	}
	// The parent report itself is untouched.
	if parentReport.Narrative != "parent" {
		t.Errorf("parent report mutated: %+v", parentReport)
	}
}

// TestForkedRunReportWithoutParentReportStaysUnlinked pins the honest-empty branch: a forked run
// whose parent has no materialized report yet (e.g. the parent is still running) records NO parent
// link rather than fabricating one.
func TestForkedRunReportWithoutParentReportStaysUnlinked(t *testing.T) {
	executor, runs, _, handle, modelClient, _, reports := newTestExecutor()
	executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID: "d5f3e2a1b9c8472d9a1b2c3d4e5f60718", OriginalFilename: "sales.csv",
		IngestStatus: domain.IngestComplete,
	}}}
	executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {{Name: "region", InferredType: domain.TypeString}},
	}}
	handle.sumRows = [][]any{{float64(0)}}
	handle.countRows = [][]any{{int64(0)}}
	// No parent report exists: byRunID has no "run-parent" entry.

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"select all"}`,
		`{"sql":"SELECT region FROM dataset_sales_csv_d5f3e2a1"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","title":"Regions"}`,
		`{"narrative":"All regions."}`,
	)

	forked := testRun()
	forked.ParentRunID = "run-parent"
	runs.run = forked

	executor.Submit("user-1", forked)
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete", got)
	}
	created := reports.created()
	if len(created) != 1 {
		t.Fatalf("created reports = %d, want 1", len(created))
	}
	if created[0].ParentReportID != "" {
		t.Errorf("forked report parent_report_id = %q, want empty — no parent report exists", created[0].ParentReportID)
	}
}
