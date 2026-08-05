package engine

import (
	"context"
	"io"
	"log/slog"
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
