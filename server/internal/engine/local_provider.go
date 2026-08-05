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
	// acquiring is true for the span between an Acquire deciding the engine needs to be (re)opened
	// and ensureOpen returning — what Provider.Status reports as StatusWaking. FEAT-011's honest-
	// status principle requires this to be observable even though the local implementation's own
	// open is fast; a future remote/pooled Provider implementation behind the same seam may take
	// much longer, and callers must never be able to tell the difference from Status's vocabulary
	// alone.
	acquiring bool

	// notify is called on every observed sleeping/waking/ready transition for this session, or nil
	// if the Provider was constructed without WithStatusChangeHook. See LocalProvider's doc comment
	// on why this is a hook rather than a direct persistence call from this package.
	notify StatusChangeFunc
}

func newSession(workspaceID string, notify StatusChangeFunc) *session {
	return &session{workspaceID: workspaceID, sem: make(chan struct{}, 1), notify: notify}
}

// setAcquiring records whether this session is currently in the span between deciding to open the
// engine and that open completing.
func (s *session) setAcquiring(v bool) {
	s.mu.Lock()
	s.acquiring = v
	s.mu.Unlock()
}

// isAcquiring reports the in-flight-open state Provider.Status surfaces as StatusWaking.
func (s *session) isAcquiring() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.acquiring
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
	if s.db == nil || s.generation != expectGeneration {
		s.mu.Unlock()
		return
	}
	err := s.db.Close()
	s.db = nil
	notify := s.notify
	s.mu.Unlock()

	if err != nil {
		logger.Error("closing idle engine", slog.String("workspace_id", s.workspaceID), slog.String("error", err.Error()))
	}
	// Notified AFTER releasing s.mu: the hook may itself call back into this Provider (e.g. a test
	// asserting Status()), and Status takes p.mu, never s.mu directly, so there is no lock-ordering
	// hazard here — this is simply hygiene against holding a lock across an external callback.
	if notify != nil {
		notify(s.workspaceID, StatusSleeping)
	}
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

// StatusChangeFunc is notified whenever a workspace's engine transitions between sleeping, waking,
// and ready.
//
// Called synchronously from whichever goroutine drove the transition (Acquire or an idle-teardown
// timer firing) and must not block or panic — a slow or failing hook must never stall the engine
// lifecycle itself. Implementations that need to do I/O (e.g. persisting to Postgres) should apply
// their own timeout and treat failure as log-and-continue, exactly as
// postgres.WorkspaceRepository.EngineStatusHook does.
type StatusChangeFunc func(workspaceID string, status Status)

// Option configures optional LocalProvider behavior beyond its required constructor parameters.
//
// Additive by design: NewLocalProvider's existing positional parameters (idleTimeout, logger) never
// change shape, so every existing call site — including internal/ingest's tests, which construct a
// LocalProvider directly — keeps compiling unchanged as options are added.
type Option func(*LocalProvider)

// WithStatusChangeHook registers fn to be notified on every observed engine status transition, for
// every workspace this Provider serves.
//
// This exists so a persisted record (e.g. workspaces.engine_status, read by screens like Workspace
// List that display many workspaces without querying this Provider per row) can be kept honest as a
// side effect of the transition, rather than staying frozen at whatever it was written as. The
// Provider itself has no persistence dependency — it only calls the hook it was given, keeping
// internal/engine's only job the engine lifecycle itself.
func WithStatusChangeHook(fn StatusChangeFunc) Option {
	return func(p *LocalProvider) {
		p.onStatusChange = fn
	}
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
	// onStatusChange is nil unless WithStatusChangeHook was supplied; notifyStatus below is a no-op
	// in that case.
	onStatusChange StatusChangeFunc
}

// NewLocalProvider constructs a LocalProvider. idleTimeout is config.Config.EngineIdleTimeout —
// architecture.md's ~10 minute idle-teardown threshold.
func NewLocalProvider(idleTimeout time.Duration, logger *slog.Logger, opts ...Option) *LocalProvider {
	p := &LocalProvider{
		sessions:    make(map[string]*session),
		idleTimeout: idleTimeout,
		logger:      logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// notifyStatus calls the configured status-change hook, if any.
func (p *LocalProvider) notifyStatus(workspaceID string, status Status) {
	if p.onStatusChange != nil {
		p.onStatusChange(workspaceID, status)
	}
}

func (p *LocalProvider) sessionFor(workspaceID string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[workspaceID]
	if !ok {
		s = newSession(workspaceID, p.onStatusChange)
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

	// Only the actually-cold path is a real wake: re-acquiring an engine that never went to sleep
	// (the common case — most questions arrive while the previous one is still warm) must not flash
	// a spurious "waking" at the status pill.
	wasOpen := s.isOpen()
	if !wasOpen {
		s.setAcquiring(true)
		p.notifyStatus(workspaceID, StatusWaking)
	}

	err := s.ensureOpen()
	s.setAcquiring(false)

	if err != nil {
		<-s.sem // free the slot we took above — this Acquire never took the engine
		return nil, fmt.Errorf("%w: %w", ErrEngineUnavailable, err)
	}

	if !wasOpen {
		p.notifyStatus(workspaceID, StatusReady)
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
// being looked at. It does not even create a session map entry for an unseen workspace, precisely so
// that "being read" and "having ever been acquired" stay observably distinct.
func (p *LocalProvider) Status(ctx context.Context, workspaceID string) (Status, error) {
	p.mu.Lock()
	s, ok := p.sessions[workspaceID]
	p.mu.Unlock()

	if !ok {
		return StatusSleeping, nil
	}
	if s.isAcquiring() {
		return StatusWaking, nil
	}
	if s.isOpen() {
		return StatusReady, nil
	}
	return StatusSleeping, nil
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
