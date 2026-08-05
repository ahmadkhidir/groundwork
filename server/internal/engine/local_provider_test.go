package engine

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLocalProviderStatusStartsSleeping(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())

	status, err := p.Status(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != StatusSleeping {
		t.Errorf("status = %q, want %q for a workspace never acquired", status, StatusSleeping)
	}
}

func TestLocalProviderAcquireThenStatusReady(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())

	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer p.Release(handle)

	if handle.WorkspaceID() != "ws-1" {
		t.Errorf("WorkspaceID() = %q, want ws-1", handle.WorkspaceID())
	}

	status, err := p.Status(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != StatusReady {
		t.Errorf("status = %q, want %q once acquired", status, StatusReady)
	}
}

func TestLocalProviderQueryRunsAgainstDuckDB(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())
	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer p.Release(handle)

	rows, err := handle.Query(context.Background(), "SELECT 1 AS n, 'hi' AS s")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	if got := rows.Columns(); len(got) != 2 || got[0] != "n" || got[1] != "s" {
		t.Fatalf("Columns() = %v, want [n s]", got)
	}

	if !rows.Next() {
		t.Fatal("expected one row")
	}
	var n int
	var s string
	if err := rows.Scan(&n, &s); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if n != 1 || s != "hi" {
		t.Errorf("row = (%d, %q), want (1, \"hi\")", n, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
}

func TestLocalProviderExecRunsDDLAndCopy(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())
	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer p.Release(handle)

	if _, err := handle.Exec(context.Background(), "CREATE TABLE t (a INTEGER)"); err != nil {
		t.Fatalf("Exec CREATE TABLE: %v", err)
	}
	if _, err := handle.Exec(context.Background(), "INSERT INTO t VALUES (1), (2), (3)"); err != nil {
		t.Fatalf("Exec INSERT: %v", err)
	}

	rows, err := handle.Query(context.Background(), "SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected one row")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestLocalProviderReleaseWithoutAcquireErrors(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())
	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := p.Release(handle); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := p.Release(handle); err == nil {
		t.Error("second Release on an already-released handle should error, not silently succeed")
	}
}

func TestLocalProviderSecondAcquireBlocksUntilRelease(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())
	first, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := p.Acquire(context.Background(), "ws-1")
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			close(acquired)
			return
		}
		defer p.Release(second)
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire returned before the first was released")
	case <-time.After(100 * time.Millisecond):
		// Expected: still blocked.
	}

	if err := p.Release(first); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire never unblocked after Release")
	}
}

func TestLocalProviderAcquireRespectsContextCancellation(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())
	first, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer p.Release(first)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = p.Acquire(ctx, "ws-1")
	if err == nil {
		t.Fatal("expected Acquire to fail once the context was cancelled while waiting")
	}
}

func TestLocalProviderIdleTeardownSleepsTheEngine(t *testing.T) {
	p := NewLocalProvider(20*time.Millisecond, testLogger())
	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := p.Release(handle); err != nil {
		t.Fatalf("Release: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := p.Status(context.Background(), "ws-1")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status == StatusSleeping {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("engine never tore down after idling past the timeout")
}

func TestLocalProviderReacquireAfterIdleTeardownReopens(t *testing.T) {
	p := NewLocalProvider(10*time.Millisecond, testLogger())
	first, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := p.Release(first); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Wait past the idle timeout so the engine tears down.
	time.Sleep(200 * time.Millisecond)

	second, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("second Acquire after idle teardown: %v", err)
	}
	defer p.Release(second)

	rows, err := second.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("Query on reopened engine: %v", err)
	}
	_ = rows.Close()
}

// FEAT-011: Engine Sleep/Wake Lifecycle & Honest Status.

// statusRecorder is a thread-safe StatusChangeFunc collector, for asserting the exact sequence of
// transitions a test scenario produced.
type statusRecorder struct {
	mu     sync.Mutex
	events []Status
}

func (r *statusRecorder) record(_ string, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, status)
}

func (r *statusRecorder) snapshot() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Status(nil), r.events...)
}

func statusesEqual(got, want []Status) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestLocalProviderStatusNeverCreatesASession is a regression test for Status.(Provider)'s own
// contract: reading status must never itself trigger a wake, not even as an incidental side effect
// of allocating bookkeeping for a workspace nobody has acquired yet.
func TestLocalProviderStatusNeverCreatesASession(t *testing.T) {
	p := NewLocalProvider(time.Minute, testLogger())

	for range 5 {
		status, err := p.Status(context.Background(), "ws-never-touched")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status != StatusSleeping {
			t.Fatalf("status = %q, want %q", status, StatusSleeping)
		}
	}

	p.mu.Lock()
	n := len(p.sessions)
	p.mu.Unlock()
	if n != 0 {
		t.Errorf("Status created %d session(s) for a workspace never Acquired; Status must have zero side effects", n)
	}
}

// TestLocalProviderNotifiesWakingThenReadyOnFirstAcquire verifies the honest-status transition
// sequence a genuinely cold engine goes through, observed via WithStatusChangeHook rather than by
// racing Status against Acquire's fast local open — the hook is the deterministic seam for this,
// since the local implementation's own open is too quick to reliably catch mid-flight from another
// goroutine.
func TestLocalProviderNotifiesWakingThenReadyOnFirstAcquire(t *testing.T) {
	rec := &statusRecorder{}
	p := NewLocalProvider(time.Minute, testLogger(), WithStatusChangeHook(rec.record))

	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer p.Release(handle)

	want := []Status{StatusWaking, StatusReady}
	if got := rec.snapshot(); !statusesEqual(got, want) {
		t.Errorf("transitions = %v, want %v", got, want)
	}
}

// TestLocalProviderReacquireWhileWarmDoesNotRenotifyWaking guards against a spurious "waking" flash
// on the status pill every time a second question reuses an already-open engine — only a genuinely
// cold Acquire is a real wake.
func TestLocalProviderReacquireWhileWarmDoesNotRenotifyWaking(t *testing.T) {
	rec := &statusRecorder{}
	p := NewLocalProvider(time.Minute, testLogger(), WithStatusChangeHook(rec.record))

	first, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := p.Release(first); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	defer p.Release(second)

	want := []Status{StatusWaking, StatusReady} // only from the first Acquire
	if got := rec.snapshot(); !statusesEqual(got, want) {
		t.Errorf("transitions = %v, want %v (re-acquiring a still-warm engine must not notify again)", got, want)
	}
}

// TestLocalProviderNotifiesSleepingOnIdleTeardown confirms the hook also fires for the teardown
// direction, which is what keeps a persisted mirror (e.g. workspaces.engine_status) from staying
// stuck at "ready" forever after the engine actually idles out.
func TestLocalProviderNotifiesSleepingOnIdleTeardown(t *testing.T) {
	rec := &statusRecorder{}
	p := NewLocalProvider(20*time.Millisecond, testLogger(), WithStatusChangeHook(rec.record))

	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := p.Release(handle); err != nil {
		t.Fatalf("Release: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := rec.snapshot()
		if len(got) == 3 {
			want := []Status{StatusWaking, StatusReady, StatusSleeping}
			if !statusesEqual(got, want) {
				t.Fatalf("transitions = %v, want %v", got, want)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected 3 transitions (waking, ready, sleeping), got %v", rec.snapshot())
}

// TestLocalProviderIdleTimeoutNeverFiresWhileHandleIsLeased is the direct test for FEAT-011's most
// important safety property: teardown must never happen while a caller (in production, the ingest
// pipeline for the duration of an ingest) still holds the lease, no matter how long it holds it past
// the idle threshold. Idle teardown only ever races the clock starting at Release, never at Acquire.
func TestLocalProviderIdleTimeoutNeverFiresWhileHandleIsLeased(t *testing.T) {
	p := NewLocalProvider(10*time.Millisecond, testLogger())

	handle, err := p.Acquire(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Sleep well past the idle timeout while still holding the handle, unreleased.
	time.Sleep(200 * time.Millisecond)

	status, err := p.Status(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != StatusReady {
		t.Fatalf("status = %q while handle is still leased; idle teardown must never fire on a leased engine", status)
	}

	// The engine must still genuinely be usable, not merely reporting Ready.
	rows, err := handle.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("Query on supposedly-still-live engine: %v", err)
	}
	_ = rows.Close()

	if err := p.Release(handle); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
