// Local-process DuckDB implementation of Provider — architecture.md's "implementation #1" of the
// Engine Provider seam, not a mock or a placeholder. It runs the engine inside this API service's own
// process (one *sql.DB per workspace) rather than as a separately addressed Fly Machine, which is the
// deployment-topology detail a later implementation of this same seam would change without touching a
// single caller.
package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	// Registers the "duckdb" database/sql driver as a side effect of import. cgo-based — see
	// IMPLEMENTATION.md §2/§8 for the build requirements this brings (CGO_ENABLED=1, a C compiler,
	// and on Windows the dynamic-link build tag the Makefile applies).
	_ "github.com/duckdb/duckdb-go/v2"
)

// provisioningDeadline bounds how long Acquire waits for another caller in the same workspace to
// release the engine before giving up. It is not a "cold start" deadline — opening an in-process
// DuckDB connection is fast — it exists only to fail an indefinitely queued Acquire with an honest
// ErrEngineUnavailable rather than blocking a request forever.
const provisioningDeadline = 30 * time.Second

// session is one workspace's DuckDB connection plus its lease and idle-teardown bookkeeping.
type session struct {
	workspaceID string

	// sem is a capacity-1 semaphore standing in for a lock that supports context-aware, timed
	// acquisition (sync.Mutex does not). Acquire sends into it; Release receives from it.
	sem chan struct{}

	mu         sync.RWMutex
	db         *sql.DB
	acquiredAt time.Time
	idleTimer  *time.Timer
	// generation guards against a delayed idle-timeout firing after the session has already been
	// re-provisioned by a fresh Acquire — the timer captures the generation it was armed for and
	// only tears down if the session is still on that generation when it fires.
	generation int
}

func newSession(workspaceID string) *session {
	return &session{workspaceID: workspaceID, sem: make(chan struct{}, 1)}
}

// ensureOpen opens the DuckDB connection if this is the first Acquire since provisioning or the last
// idle teardown. In-memory: the local implementation holds no data of its own between sessions —
// re-attaching Parquet views from object storage on wake is the Session Query path's job (FEAT-013+,
// a later roadmap phase), not the Ingest Pipeline's, and is deliberately not built here.
func (s *session) ensureOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return nil
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return fmt.Errorf("opening duckdb: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return fmt.Errorf("pinging duckdb: %w", err)
	}

	s.db = db
	s.generation++
	return nil
}

// isOpen reports whether the session currently holds a live connection, without provisioning one —
// what Provider.Status reads.
func (s *session) isOpen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db != nil
}

// teardown closes the connection if the session is still on the generation the caller expects,
// guarding against a stale idle timer racing a fresh Acquire.
func (s *session) teardown(expectGeneration int, logger *slog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil || s.generation != expectGeneration {
		return
	}
	if err := s.db.Close(); err != nil {
		logger.Error("closing idle engine", slog.String("workspace_id", s.workspaceID), slog.String("error", err.Error()))
	}
	s.db = nil
}

func (s *session) armIdleTimer(idleTimeout time.Duration, logger *slog.Logger) {
	s.mu.Lock()
	generation := s.generation
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.idleTimer = time.AfterFunc(idleTimeout, func() {
		s.teardown(generation, logger)
	})
	s.mu.Unlock()
}

func (s *session) cancelIdleTimer() {
	s.mu.Lock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.mu.Unlock()
}

// LocalProvider is a Provider backed by one in-process DuckDB connection per workspace.
//
// Safe for concurrent use across many workspaces: the top-level map is guarded by its own mutex, and
// each workspace's session serializes its own lease independently, so one workspace's Acquire never
// blocks on another's.
type LocalProvider struct {
	mu          sync.Mutex
	sessions    map[string]*session
	idleTimeout time.Duration
	logger      *slog.Logger
}

// NewLocalProvider constructs a LocalProvider. idleTimeout is config.Config.EngineIdleTimeout —
// architecture.md's ~10 minute idle-teardown threshold.
func NewLocalProvider(idleTimeout time.Duration, logger *slog.Logger) *LocalProvider {
	return &LocalProvider{
		sessions:    make(map[string]*session),
		idleTimeout: idleTimeout,
		logger:      logger,
	}
}

func (p *LocalProvider) sessionFor(workspaceID string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[workspaceID]
	if !ok {
		s = newSession(workspaceID)
		p.sessions[workspaceID] = s
	}
	return s
}

// Acquire implements Provider.
func (p *LocalProvider) Acquire(ctx context.Context, workspaceID string) (Handle, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("engine: workspaceID is required")
	}
	s := p.sessionFor(workspaceID)

	deadline := time.NewTimer(provisioningDeadline)
	defer deadline.Stop()

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrEngineUnavailable, ctx.Err())
	case <-deadline.C:
		return nil, ErrEngineUnavailable
	}

	s.cancelIdleTimer()

	if err := s.ensureOpen(); err != nil {
		<-s.sem // free the slot we took above — this Acquire never took the engine
		return nil, fmt.Errorf("%w: %w", ErrEngineUnavailable, err)
	}

	s.mu.Lock()
	s.acquiredAt = time.Now()
	s.mu.Unlock()

	return &localHandle{session: s}, nil
}

// Release implements Provider.
func (p *LocalProvider) Release(handle Handle) error {
	h, ok := handle.(*localHandle)
	if !ok {
		return fmt.Errorf("engine: handle was not acquired from LocalProvider")
	}

	h.session.armIdleTimer(p.idleTimeout, p.logger)

	select {
	case <-h.session.sem:
	default:
		return fmt.Errorf("engine: handle for workspace %s was already released", h.session.workspaceID)
	}
	return nil
}

// Status implements Provider. It never provisions — a workspace with no session yet, or one whose
// engine has idled out, honestly reports sleeping rather than triggering a wake as a side effect of
// being looked at.
func (p *LocalProvider) Status(ctx context.Context, workspaceID string) (Status, error) {
	p.mu.Lock()
	s, ok := p.sessions[workspaceID]
	p.mu.Unlock()

	if !ok || !s.isOpen() {
		return StatusSleeping, nil
	}
	return StatusReady, nil
}

// localHandle is the Handle LocalProvider hands out. Not safe for concurrent use by multiple
// callers, per Handle's doc comment — the session's semaphore is what enforces that at the Provider
// level; a Handle instance itself does no additional locking of its own.
type localHandle struct {
	session *session
}

// WorkspaceID implements Handle.
func (h *localHandle) WorkspaceID() string {
	return h.session.workspaceID
}

// AcquiredAt implements Handle.
func (h *localHandle) AcquiredAt() time.Time {
	h.session.mu.RLock()
	defer h.session.mu.RUnlock()
	return h.session.acquiredAt
}

// Query implements Handle.
func (h *localHandle) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	h.session.mu.RLock()
	db := h.session.db
	h.session.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("engine: handle for workspace %s is no longer live", h.session.workspaceID)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("engine: query: %w", err)
	}
	return &sqlRows{rows: rows}, nil
}

// Exec implements Handle.
func (h *localHandle) Exec(ctx context.Context, statement string, args ...any) (int64, error) {
	h.session.mu.RLock()
	db := h.session.db
	h.session.mu.RUnlock()
	if db == nil {
		return 0, fmt.Errorf("engine: handle for workspace %s is no longer live", h.session.workspaceID)
	}

	result, err := db.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, fmt.Errorf("engine: exec: %w", err)
	}
	// Not every DuckDB statement (e.g. INSTALL/LOAD, COPY) reports a meaningful affected-row count;
	// treat that as "unknown" (0) rather than a failure.
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return affected, nil
}

// sqlRows adapts database/sql.Rows to the narrower Rows seam so callers never see a driver-shaped
// API. The only real translation needed is Columns(), whose database/sql signature returns an error
// this seam's callers have no use for — a column-listing failure here means the query already failed
// in a way Err() will also report.
type sqlRows struct {
	rows *sql.Rows
}

func (r *sqlRows) Next() bool { return r.rows.Next() }

func (r *sqlRows) Scan(dest ...any) error { return r.rows.Scan(dest...) }

func (r *sqlRows) Columns() []string {
	cols, err := r.rows.Columns()
	if err != nil {
		return nil
	}
	return cols
}

func (r *sqlRows) Err() error { return r.rows.Err() }

func (r *sqlRows) Close() error { return r.rows.Close() }
