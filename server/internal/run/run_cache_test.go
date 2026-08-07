package run

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
	"github.com/ahmadkhidir/groundwork/server/internal/orchestrator"
)

// seedDatasetExecutor wires a single complete dataset (+ columns) into the executor so the pipeline
// can assemble tables (schema digest) and execute SQL against the fake handle.
func seedDatasetExecutor(e *Executor) {
	e.datasets = &fakeDatasetStore{datasets: []domain.Dataset{{
		ID:               "d5f3e2a1b9c8472d9a1b2c3d4e5f60718",
		WorkspaceID:      "ws-1",
		OriginalFilename: "Sales Data (Q3).csv",
		IngestStatus:     domain.IngestComplete,
	}}}
	e.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
		"d5f3e2a1b9c8472d9a1b2c3d4e5f60718": {
			{Name: "region", InferredType: domain.TypeString},
			{Name: "amount", InferredType: domain.TypeNumber},
		},
	}}
}

// fakeCache is an orchestrator.CacheStore. By default Get returns the argument-independent record set
// via `record` when `hit` is true, else orchestrator.ErrCacheMiss; Put records its calls.
type fakeCache struct {
	mu       sync.Mutex
	hit      bool
	record   orchestrator.CacheRecord
	records  []orchestrator.CacheRecord
	puts     int
	getCalls int
	getErr   error
	putErr   error
}

func (f *fakeCache) Get(_ context.Context, _, _, _, _ string) (orchestrator.CacheRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return orchestrator.CacheRecord{}, f.getErr
	}
	if !f.hit {
		return orchestrator.CacheRecord{}, orchestrator.ErrCacheMiss
	}
	return f.record, nil
}

func (f *fakeCache) Put(_ context.Context, record orchestrator.CacheRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.records = append(f.records, record)
	return f.putErr
}

func (f *fakeCache) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

// cacheRecord builds a full, self-consistent cache record whose outputs replay into a complete run.
func cacheRecord(srcRunID string) orchestrator.CacheRecord {
	return orchestrator.CacheRecord{
		WorkspaceID:        "ws-1",
		SourceRunID:        srcRunID,
		PlanOutput:         `{"answerable":true,"reason":"","approach":"group by region"}`,
		GenerateSQLOutput:  `{"sql":"SELECT region, COUNT(*) AS cnt FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"}`,
		ExecuteOutput:      `{"columns":["region","cnt"],"rows":[["North",10]],"row_count":1}`,
		ChartSpecOutput:    `{"chart_type":"table","x_axis_column":"region","y_axis_column":"","series_column":"","title":"Customers by region"}`,
		NarrateOutput:      `{"narrative":"North leads the count."}`,
		ReconciliationJSON: `{"rows_in":12,"rows_out":1,"exclusion_warnings":[]}`,
	}
}

// TestCacheMissFallsThroughToNormalExecution verifies FEAT-027's miss path: a run that is not cached
// falls through to a normal full execution (model called for every step, engine acquired, report
// materialized) — the cache is purely additive and never suppresses the pipeline.
func TestCacheMissFallsThroughToNormalExecution(t *testing.T) {
	executor, runs, _, _, modelClient, _, reports := newTestExecutor()
	seedDatasetExecutor(executor)
	executor.cache = &fakeCache{hit: false} // miss → normal execution

	modelClient.enqueue(
		`{"answerable":true,"reason":"","approach":"group by region"}`,
		`{"sql":"SELECT region FROM dataset_sales_data_q3_csv_d5f3e2a1 GROUP BY region"}`,
		`{"matches":true,"verdict":"ok","severity":"ok"}`,
		`{"chart_type":"table","x_axis_column":"region","y_axis_column":"","series_column":"","title":"x"}`,
		`{"narrative":"North leads."}`,
	)

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status.IsTerminal() })

	if got := runs.current().Status; got != domain.RunComplete {
		t.Fatalf("run status = %s, want complete on a cache miss", got)
	}
	// The miss still executes normally: plan/generate/adversarial/chart/narrate each hit the model.
	if got := modelClient.callCount(); got != 5 {
		t.Fatalf("model calls = %d, want 5 (normal execution on miss, adversarial review included)", got)
	}
	if got := len(reports.created()); got != 1 {
		t.Fatalf("reports = %d, want 1 on normal execution", got)
	}
}

// TestCacheHitReplaysWithoutModelOrEngine verifies FEAT-027's fast path: a deterministically
// identical run answers entirely from cache — no model calls, no engine acquisition — and
// materializes a report from the cached outputs, publishing the cache.hit live event so the viewer
// can surface "cached from run".
func TestCacheHitReplaysWithoutModelOrEngine(t *testing.T) {
	executor, runs, _, _, modelClient, _, reports := newTestExecutor()
	seedDatasetExecutor(executor)

	hub := events.NewHub()
	executor.hub = hub
	executor.cache = &fakeCache{hit: true, record: cacheRecord("run-9")}

	ch, unsubscribe := hub.Subscribe("run-1")
	defer unsubscribe()

	executor.Submit("user-1", testRun())
	waitFor(t, func() bool { return runs.current().Status == domain.RunComplete })

	// The cache fast path never asks the model or acquires the engine.
	if got := modelClient.callCount(); got != 0 {
		t.Fatalf("model calls = %d, want 0 on a cache hit", got)
	}
	// A report is materialized from the replayed cached outputs.
	if got := len(reports.created()); got != 1 {
		t.Fatalf("reports = %d, want 1 from cache", got)
	}
	// cache.hit is published so the viewer can surface the "cached from run" reference.
	if ev := waitForEvent(t, ch, events.TypeCacheHit); ev == nil || ev.CachedRunID != "run-9" {
		t.Fatalf("expected a cache.hit event with cached_run_id run-9, got %+v", ev)
	}
}

// waitForEvent drains ch until an event matching typ arrives or the deadline passes.
func waitForEvent(t *testing.T, ch <-chan events.Event, typ events.Type) *events.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-ch:
			if ev.Type == typ {
				return &ev
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

// TestSchemaDigestInvalidatesOnDataChange pins FEAT-027's determinism contract: the SAME workspace
// state hashes to the SAME key (so an identical question hits the cache), and any change to the
// underlying DATA — a re-ingested row count, a new dataset row — changes the key (AC2: "invalidated
// automatically when the underlying schema or data changes"). The digest mixes the schema surface
// with a per-dataset data-version stamp (id + row_count + created_at), so this is asserted at the
// key level, not just on the schema shape.
func TestSchemaDigestInvalidatesOnDataChange(t *testing.T) {
	executor, _, _, _, _, _, _ := newTestExecutor()
	base := domain.Dataset{
		ID: "d1", WorkspaceID: "ws-1", OriginalFilename: "Sales.csv",
		IngestStatus: domain.IngestComplete, RowCount: 1000,
		CreatedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
	}
	mk := func(ds domain.Dataset) {
		executor.datasets = &fakeDatasetStore{datasets: []domain.Dataset{ds}}
		executor.columns = &fakeColumnStore{byDataset: map[string][]domain.Column{
			ds.ID: {{Name: "region", InferredType: domain.TypeString}},
		}}
	}

	mk(base)
	first, err := executor.schemaDigest(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("schemaDigest: %v", err)
	}

	// Identical state → identical key (AC1's same-question-same-data hit path).
	mk(base)
	second, err := executor.schemaDigest(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("schemaDigest: %v", err)
	}
	if first != second {
		t.Errorf("identical state hashed differently: %s != %s — cache would miss a same-question same-data hit", first, second)
	}

	// The DATA changed (row count) while the schema surface is byte-identical: the key MUST change
	// so the cache cannot serve a stale answer.
	changed := base
	changed.RowCount = 1001
	mk(changed)
	if got, err := executor.schemaDigest(context.Background(), "ws-1"); err != nil {
		t.Fatalf("schemaDigest: %v", err)
	} else if got == first {
		t.Error("digest unchanged after data row_count changed — FEAT-027 AC2 data invalidation broken")
	}

	// A brand-new dataset (Replace / Keep-both land as a new row) must also change the key.
	mk(domain.Dataset{
		ID: "d2", WorkspaceID: "ws-1", OriginalFilename: "Sales.csv",
		IngestStatus: domain.IngestComplete, RowCount: 999,
		CreatedAt: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC),
	})
	if got, err := executor.schemaDigest(context.Background(), "ws-1"); err != nil {
		t.Fatalf("schemaDigest: %v", err)
	} else if got == first {
		t.Error("digest unchanged after a new dataset row — cache must invalidate on re-upload/replace/delete")
	}
}
