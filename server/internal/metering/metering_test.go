package metering

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// PeriodFor — the period every source records into.
// ---------------------------------------------------------------------------

func TestPeriodForMidMonth(t *testing.T) {
	got := time.Date(2026, 8, 15, 14, 30, 0, 0, time.UTC)
	start, end := PeriodFor(got)
	wantStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Errorf("PeriodFor(%v) = [%v, %v), want [%v, %v)", got, start, end, wantStart, wantEnd)
	}
}

func TestPeriodForMonthBoundaries(t *testing.T) {
	// First instant of the month stays in that month; the last instant rolls into the next.
	cases := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{"month start", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"month end 23:59:59", time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, _ := PeriodFor(tc.in)
			if !start.Equal(tc.want) {
				t.Errorf("PeriodFor(%v) start = %v, want %v", tc.in, start, tc.want)
			}
		})
	}
}

func TestPeriodForYearRollover(t *testing.T) {
	start, end := PeriodFor(time.Date(2026, 12, 25, 10, 0, 0, 0, time.UTC))
	if !start.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2026-12-01", start)
	}
	if !end.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2027-01-01 (year rollover)", end)
	}
}

func TestPeriodForLeapYearFebruary(t *testing.T) {
	// 2024 is a leap year: February has 29 days, so the month ends 2024-03-01.
	start, end := PeriodFor(time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC))
	if !start.Equal(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2024-02-01", start)
	}
	if !end.Equal(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2024-03-01 (leap February)", end)
	}

	// 2023 is not: February has 28 days.
	start, end = PeriodFor(time.Date(2023, 2, 15, 0, 0, 0, 0, time.UTC))
	if !end.Equal(time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2023-03-01 (non-leap February)", end)
	}
}

func TestPeriodForNormalizesToUTC(t *testing.T) {
	// A timestamp in a non-UTC zone must still resolve to the UTC calendar month — a source in
	// UTC-8 on 2026-08-01 00:30 local is 2026-08-01 08:30 UTC, so the period starts at that UTC
	// month's first instant.
	loc := time.FixedZone("west", -8*60*60)
	in := time.Date(2026, 8, 1, 0, 30, 0, 0, loc)
	start, end := PeriodFor(in)
	if !start.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2026-08-01T00:00:00Z", start)
	}
	if !end.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2026-09-01T00:00:00Z", end)
	}
}

// ---------------------------------------------------------------------------
// EngineSeconds — the lease-duration meter.
// ---------------------------------------------------------------------------

func TestEngineSecondsFractional(t *testing.T) {
	acquired := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	released := acquired.Add(1500 * time.Millisecond)
	if got := EngineSeconds(acquired, released); got != 1.5 {
		t.Errorf("EngineSeconds = %v, want 1.5", got)
	}
}

func TestEngineSecondsZeroDuration(t *testing.T) {
	acquired := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	if got := EngineSeconds(acquired, acquired); got != 0 {
		t.Errorf("EngineSeconds(zero) = %v, want 0", got)
	}
}

func TestEngineSecondsNegativeIsZero(t *testing.T) {
	// Clock skew (AcquiredAt after now) must read as 0, never a negative meter that would
	// silently reduce a workspace's billed engine-seconds.
	acquired := time.Date(2026, 8, 7, 10, 0, 5, 0, time.UTC)
	released := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	if got := EngineSeconds(acquired, released); got != 0 {
		t.Errorf("EngineSeconds(negative) = %v, want 0", got)
	}
}

func TestEngineSecondsWholeMinutes(t *testing.T) {
	acquired := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	released := time.Date(2026, 8, 7, 10, 2, 0, 0, time.UTC)
	if got := EngineSeconds(acquired, released); got != 120 {
		t.Errorf("EngineSeconds = %v, want 120", got)
	}
}

// ---------------------------------------------------------------------------
// UsageWriter dispatch — period boundaries are computed by the writer, not the source.
// ---------------------------------------------------------------------------

// fakeUpserter is an in-memory UsageUpserter that MIRRORS the SQL semantics of
// postgres.UsageRepository.Upsert / SetStorageBytes (accumulate vs replace), so the
// accumulate-vs-replace contract can be pinned without a database. The store's SQL statements are
// documented to implement exactly these semantics; see store/postgres/usage.go.
type fakeUpserter struct {
	mu      sync.Mutex
	records map[string]*fakeRecord // keyed by workspaceID|periodStart
	calls   []string
}

type fakeRecord struct {
	workspaceID   string
	periodStart   time.Time
	periodEnd     time.Time
	engineSeconds float64
	modelTokens   int64
	storageBytes  int64
}

func newFakeUpserter() *fakeUpserter {
	return &fakeUpserter{records: make(map[string]*fakeRecord)}
}

func key(workspaceID string, periodStart time.Time) string {
	return workspaceID + "|" + periodStart.String()
}

// Upsert mirrors the accumulate statement: engine_seconds and model_tokens add, storage_bytes is
// untouched (the INSERT carries 0 and the conflict update never mentions it).
func (f *fakeUpserter) Upsert(_ context.Context, workspaceID string, periodStart, periodEnd time.Time, engineSeconds float64, modelTokens int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(workspaceID, periodStart)
	rec, ok := f.records[k]
	if !ok {
		rec = &fakeRecord{workspaceID: workspaceID, periodStart: periodStart, periodEnd: periodEnd}
		f.records[k] = rec
	}
	rec.engineSeconds += engineSeconds
	rec.modelTokens += modelTokens
	f.calls = append(f.calls, "upsert")
	return nil
}

// SetStorageBytes mirrors the replace statement: storage_bytes is overwritten, engine_seconds and
// model_tokens are untouched (the INSERT carries 0 and the conflict update never mentions them).
func (f *fakeUpserter) SetStorageBytes(_ context.Context, workspaceID string, periodStart, periodEnd time.Time, bytes int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(workspaceID, periodStart)
	rec, ok := f.records[k]
	if !ok {
		rec = &fakeRecord{workspaceID: workspaceID, periodStart: periodStart, periodEnd: periodEnd}
		f.records[k] = rec
	}
	rec.storageBytes = bytes
	f.calls = append(f.calls, "set_storage")
	return nil
}

func (f *fakeUpserter) get(workspaceID string, periodStart time.Time) (*fakeRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[key(workspaceID, periodStart)]
	return rec, ok
}

// fixedNow returns a writer pinned to a known period for deterministic period assertions.
func fixedWriter(repo UsageUpserter) (*writer, time.Time) {
	now := time.Date(2026, 8, 7, 14, 30, 0, 0, time.UTC)
	return &writer{repo: repo, now: func() time.Time { return now }}, now
}

func TestRecordDispatchesToUpsertWithCurrentPeriod(t *testing.T) {
	repo := newFakeUpserter()
	w, now := fixedWriter(repo)

	if err := w.Record(context.Background(), "ws-1", 42.5, 1200); err != nil {
		t.Fatalf("Record: %v", err)
	}

	start, end := PeriodFor(now)
	rec, ok := repo.get("ws-1", start)
	if !ok {
		t.Fatal("no record was created for the current period")
	}
	if !rec.periodEnd.Equal(end) {
		t.Errorf("period_end = %v, want %v", rec.periodEnd, end)
	}
	if rec.engineSeconds != 42.5 || rec.modelTokens != 1200 {
		t.Errorf("record = %+v, want engine_seconds 42.5 and model_tokens 1200", rec)
	}
}

func TestSetStorageBytesDispatchesToReplaceWithCurrentPeriod(t *testing.T) {
	repo := newFakeUpserter()
	w, now := fixedWriter(repo)

	if err := w.SetStorageBytes(context.Background(), "ws-1", 10_000_000); err != nil {
		t.Fatalf("SetStorageBytes: %v", err)
	}

	start, _ := PeriodFor(now)
	rec, ok := repo.get("ws-1", start)
	if !ok {
		t.Fatal("no record was created for the current period")
	}
	if rec.storageBytes != 10_000_000 {
		t.Errorf("storage_bytes = %d, want 10000000", rec.storageBytes)
	}
}

// TestAccumulateContract pins the engine-seconds/model-tokens accumulation: repeated Records in
// the same period add, never replace. This is the contract the store's ON CONFLICT
// ... engine_seconds = usage_records.engine_seconds + EXCLUDED.engine_seconds implements.
func TestAccumulateContract(t *testing.T) {
	repo := newFakeUpserter()
	w, now := fixedWriter(repo)

	// Three engine runs and two model calls in the same period.
	if err := w.Record(context.Background(), "ws-1", 10.5, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), "ws-1", 20, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), "ws-1", 5.25, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), "ws-1", 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), "ws-1", 0, 500); err != nil {
		t.Fatal(err)
	}

	start, _ := PeriodFor(now)
	rec, _ := repo.get("ws-1", start)
	if rec.engineSeconds != 35.75 {
		t.Errorf("engine_seconds = %v, want 35.75 (accumulated across runs)", rec.engineSeconds)
	}
	if rec.modelTokens != 600 {
		t.Errorf("model_tokens = %d, want 600 (accumulated across calls)", rec.modelTokens)
	}
}

// TestReplaceContract pins the storage_bytes snapshot semantics: repeated SetStorageBytes calls in
// the same period REPLACE (last write wins), never accumulate — the store's ON CONFLICT
// ... storage_bytes = EXCLUDED.storage_bytes implements this.
func TestReplaceContract(t *testing.T) {
	repo := newFakeUpserter()
	w, now := fixedWriter(repo)

	if err := w.SetStorageBytes(context.Background(), "ws-1", 100); err != nil {
		t.Fatal(err)
	}
	if err := w.SetStorageBytes(context.Background(), "ws-1", 250); err != nil {
		t.Fatal(err)
	}

	start, _ := PeriodFor(now)
	rec, _ := repo.get("ws-1", start)
	if rec.storageBytes != 250 {
		t.Errorf("storage_bytes = %d, want 250 (replaced, not 350)", rec.storageBytes)
	}
}

// TestAccumulateAndReplaceDoNotClobberEachOther pins the column isolation of the two upserts: an
// engine/token write must not touch the storage snapshot, and a storage snapshot must not touch
// the accumulated engine/token totals — the INSERT column lists omit the other columns and the
// conflict updates never mention them.
func TestAccumulateAndReplaceDoNotClobberEachOther(t *testing.T) {
	repo := newFakeUpserter()
	w, now := fixedWriter(repo)

	if err := w.Record(context.Background(), "ws-1", 30, 100); err != nil {
		t.Fatal(err)
	}
	if err := w.SetStorageBytes(context.Background(), "ws-1", 5_000_000); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), "ws-1", 7, 0); err != nil {
		t.Fatal(err)
	}

	start, _ := PeriodFor(now)
	rec, _ := repo.get("ws-1", start)
	if rec.engineSeconds != 37 || rec.modelTokens != 100 {
		t.Errorf("accumulated columns = %+v, want engine 37 and tokens 100 (storage write must not clobber)", rec)
	}
	if rec.storageBytes != 5_000_000 {
		t.Errorf("storage_bytes = %d, want 5000000 (accumulate writes must not clobber the snapshot)", rec.storageBytes)
	}
}

// TestRecordPeriodIsolatedFromOtherPeriods: an event in a later month must not touch the earlier
// month's record — the UNIQUE (workspace_id, period_start) upsert keys on period_start.
func TestRecordPeriodIsolatedFromOtherPeriods(t *testing.T) {
	repo := newFakeUpserter()
	w := &writer{repo: repo, now: func() time.Time { return time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC) }}
	if err := w.Record(context.Background(), "ws-1", 10, 0); err != nil {
		t.Fatal(err)
	}
	w2 := &writer{repo: repo, now: func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }}
	if err := w2.Record(context.Background(), "ws-1", 20, 0); err != nil {
		t.Fatal(err)
	}

	aug, _ := repo.get("ws-1", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	sep, _ := repo.get("ws-1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if aug == nil || sep == nil {
		t.Fatal("expected separate records for August and September")
	}
	if aug.engineSeconds != 10 || sep.engineSeconds != 20 {
		t.Errorf("records not isolated: aug=%+v sep=%+v", aug, sep)
	}
}

// ---------------------------------------------------------------------------
// ReconcileStorage — the daily reconciliation's unit of work.
// ---------------------------------------------------------------------------

// fakeSizer is a StorageSizer test double keyed by prefix.
type fakeSizer struct {
	sizes map[string]int64
	err   error
}

func (f *fakeSizer) SumBytesByPrefix(_ context.Context, prefix string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.sizes[prefix], nil
}

// fakeWriter is a UsageWriter test double recording every SetStorageBytes call.
type fakeWriter struct {
	mu      sync.Mutex
	storage map[string]int64 // workspaceID -> last bytes written
	lastErr error
}

func (f *fakeWriter) Record(_ context.Context, _ string, _ float64, _ int64) error { return nil }

func (f *fakeWriter) SetStorageBytes(_ context.Context, workspaceID string, bytes int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastErr != nil {
		return f.lastErr
	}
	if f.storage == nil {
		f.storage = make(map[string]int64)
	}
	f.storage[workspaceID] = bytes
	return nil
}

func (f *fakeWriter) snapshot(workspaceID string) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.storage[workspaceID]
	return v, ok
}

func TestReconcileStorageSumsPrefixAndReplacesSnapshot(t *testing.T) {
	sizer := &fakeSizer{sizes: map[string]int64{
		"workspaces/ws-1/": 123456,
	}}
	writer := &fakeWriter{}

	if err := ReconcileStorage(context.Background(), "ws-1", sizer, writer); err != nil {
		t.Fatalf("ReconcileStorage: %v", err)
	}
	if got, ok := writer.snapshot("ws-1"); !ok || got != 123456 {
		t.Errorf("storage snapshot = %d (present=%v), want 123456", got, ok)
	}
}

// TestReconcileStorageUsesWorkspacePrefix pins that the reconciliation sums under the workspace
// prefix, not some other key space — a sizer keyed by prefix proves the exact prefix requested.
func TestReconcileStorageUsesWorkspacePrefix(t *testing.T) {
	requested := ""
	sizer := &fakeSizer{sizes: map[string]int64{
		"workspaces/ws-1/": 42,
	}}
	sizerWithHook := &sizerRecording{inner: sizer, onPrefix: func(prefix string) { requested = prefix }}
	writer := &fakeWriter{}

	if err := ReconcileStorage(context.Background(), "ws-1", sizerWithHook, writer); err != nil {
		t.Fatalf("ReconcileStorage: %v", err)
	}
	if requested != "workspaces/ws-1/" {
		t.Errorf("summed prefix = %q, want %q", requested, "workspaces/ws-1/")
	}
}

// sizerRecording wraps a StorageSizer and records the prefix each call requested.
type sizerRecording struct {
	inner    StorageSizer
	onPrefix func(prefix string)
}

func (s *sizerRecording) SumBytesByPrefix(ctx context.Context, prefix string) (int64, error) {
	s.onPrefix(prefix)
	return s.inner.SumBytesByPrefix(ctx, prefix)
}

// TestReconcileStorageEmptyWorkspaceIsNoOpSafe: a workspace with no objects sums to 0 and the
// snapshot is written as 0 — never an error, and a truthful point-in-time reading (schema.md's
// "as of period_end").
func TestReconcileStorageEmptyWorkspaceIsNoOpSafe(t *testing.T) {
	sizer := &fakeSizer{sizes: map[string]int64{}}
	writer := &fakeWriter{}

	if err := ReconcileStorage(context.Background(), "ws-empty", sizer, writer); err != nil {
		t.Fatalf("ReconcileStorage on empty workspace: %v", err)
	}
	if got, ok := writer.snapshot("ws-empty"); !ok || got != 0 {
		t.Errorf("storage snapshot = %d (present=%v), want 0", got, ok)
	}
}

// TestReconcileStorageIdempotent: running the same workspace twice writes the same snapshot (the
// second upsert replaces the first), so the daily sweep can never double-count against an
// event-driven reconcile.
func TestReconcileStorageIdempotent(t *testing.T) {
	sizer := &fakeSizer{sizes: map[string]int64{"workspaces/ws-1/": 999}}
	writer := &fakeWriter{}

	if err := ReconcileStorage(context.Background(), "ws-1", sizer, writer); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileStorage(context.Background(), "ws-1", sizer, writer); err != nil {
		t.Fatal(err)
	}
	if got, _ := writer.snapshot("ws-1"); got != 999 {
		t.Errorf("snapshot after two reconciles = %d, want 999 (idempotent)", got)
	}
}

func TestReconcileStorageSizerErrorPropagates(t *testing.T) {
	sizer := &fakeSizer{err: errors.New("storage down")}
	writer := &fakeWriter{}

	err := ReconcileStorage(context.Background(), "ws-1", sizer, writer)
	if err == nil {
		t.Fatal("expected the sizer error to propagate, got nil")
	}
	if _, ok := writer.snapshot("ws-1"); ok {
		t.Error("snapshot was written despite the sizer error")
	}
}

func TestReconcileStorageWriterErrorPropagates(t *testing.T) {
	sizer := &fakeSizer{sizes: map[string]int64{"workspaces/ws-1/": 5}}
	writer := &fakeWriter{lastErr: errors.New("db down")}

	err := ReconcileStorage(context.Background(), "ws-1", sizer, writer)
	if err == nil {
		t.Fatal("expected the writer error to propagate, got nil")
	}
}

// ---------------------------------------------------------------------------
// Reconciler adapter — the narrow seam event sites call.
// ---------------------------------------------------------------------------

func TestStorageReconcilerReconcileDelegates(t *testing.T) {
	sizer := &fakeSizer{sizes: map[string]int64{"workspaces/ws-1/": 777}}
	writer := &fakeWriter{}
	r := NewStorageReconciler(sizer, writer)

	if err := r.Reconcile(context.Background(), "ws-1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got, ok := writer.snapshot("ws-1"); !ok || got != 777 {
		t.Errorf("snapshot = %d (present=%v), want 777", got, ok)
	}
}
