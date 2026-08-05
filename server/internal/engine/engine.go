// Package engine defines the Engine Provider seam: the ONLY route to an analytical engine.
//
// Nothing outside this package may know how an engine is provisioned, where it runs, or that it is
// DuckDB at all. Handlers, the orchestrator, and the ingest pipeline depend on the Provider
// interface and nothing else.
//
// That indebtedness is deliberate and load-bearing. architecture.md treats local-process DuckDB as
// "implementation #1" of this seam rather than as the engine itself, precisely so a later
// implementation — a remote pool, a per-workspace container, something that is not DuckDB — can be
// swapped in without touching a single caller. A handler that reaches for a *sql.DB directly
// silently welds the product to one deployment model.
package engine

import (
	"context"
	"errors"
	"time"
)

// ErrEngineUnavailable indicates the engine could not be provisioned within the deadline.
//
// Callers surface this to the user as an honest, retryable state — never as a generic failure and
// never as an indefinite spinner.
var ErrEngineUnavailable = errors.New("engine unavailable")

// Status mirrors the engine lifecycle exposed to the user. It is intentionally the same vocabulary
// as domain.EngineStatus: what the system reports internally is what the UI shows.
type Status string

// Engine statuses.
const (
	StatusSleeping Status = "sleeping"
	StatusWaking   Status = "waking"
	StatusReady    Status = "ready"
)

// Handle is a leased, ready-to-query engine for exactly one workspace.
//
// A handle is NOT safe for concurrent use by multiple runs and must be returned via Provider.Release
// when the caller is done. Holding one open indefinitely defeats idle teardown and bills the
// workspace for engine-seconds it is not using.
type Handle interface {
	// WorkspaceID reports which workspace this handle is scoped to. Every query executed through it
	// is confined to that workspace's data.
	WorkspaceID() string

	// Query executes a read-only analytical query and returns the result rows.
	//
	// The context governs cancellation: a halted or abandoned run must actually stop the query
	// rather than let it run to completion unobserved. Implementations must honour it.
	Query(ctx context.Context, sql string, args ...any) (Rows, error)

	// Exec runs a statement that does not produce a row set — DDL, COPY, and other ingest-time
	// statements — and reports the number of rows affected where the underlying engine reports one.
	//
	// Added by FEAT-004 (Wave 3), widening the seam beyond Query deliberately rather than loosening
	// Query's read-only contract: the Ingest Pipeline genuinely needs to run CREATE/COPY/INSTALL
	// statements against the workspace's engine (normalizing an uploaded file into Parquet), and
	// Query's doc comment above scopes it to read-only analytical queries on purpose — narrating a
	// COPY statement through it would be the wrong fix, not a shortcut. Every other caller of Handle
	// (the future orchestrator's execute/generate_sql steps) is unaffected: it only ever needs Query.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)

	// AcquiredAt reports when the lease began, for engine-seconds metering.
	AcquiredAt() time.Time
}

// Rows is a minimal forward-only cursor over a query result.
//
// Deliberately narrower than database/sql.Rows so the seam does not leak a driver-shaped API to
// callers and quietly make the abstraction unswappable.
type Rows interface {
	// Next advances to the next row, reporting false when the result is exhausted.
	Next() bool

	// Scan copies the current row's columns into dest.
	Scan(dest ...any) error

	// Columns reports the result's column names, in order.
	Columns() []string

	// Err returns any error encountered during iteration. ALWAYS check it after Next returns false:
	// a truncated result and a complete one are otherwise indistinguishable.
	Err() error

	// Close releases the cursor.
	Close() error
}

// Provider provisions and reclaims session-scoped engines.
//
// Implementations must be safe for concurrent use: many workspaces wake and sleep at once.
type Provider interface {
	// Acquire leases a ready engine for the workspace, waking or provisioning one if necessary.
	//
	// It blocks until the engine is ready, the context is cancelled, or the wait exceeds the
	// implementation's provisioning deadline, in which case it returns ErrEngineUnavailable.
	//
	// Callers MUST pair every successful Acquire with a Release, conventionally deferred.
	Acquire(ctx context.Context, workspaceID string) (Handle, error)

	// Release returns a leased engine, restarting its idle-teardown clock.
	//
	// Releasing is not the same as tearing down: the engine typically stays warm so a follow-up
	// question does not pay provisioning cost again.
	Release(handle Handle) error

	// Status reports a workspace's current engine state without provisioning one.
	//
	// This is what the UI polls to render the status pill, so it must never itself trigger a wake —
	// otherwise merely looking at a workspace would bill engine-seconds.
	Status(ctx context.Context, workspaceID string) (Status, error)
}
