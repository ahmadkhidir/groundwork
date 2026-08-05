package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// ErrWorkspaceNotFound is returned when no workspace matches the given id (or the requesting user
// does not own it — the two are made indistinguishable at the API layer so a non-owner cannot probe
// for the existence of a workspace they don't have access to).
var ErrWorkspaceNotFound = errors.New("workspace not found")

// WorkspaceRepository persists and retrieves workspaces.
type WorkspaceRepository struct {
	pool *Pool
}

// NewWorkspaceRepository constructs a WorkspaceRepository backed by pool.
func NewWorkspaceRepository(pool *Pool) *WorkspaceRepository {
	return &WorkspaceRepository{pool: pool}
}

// Create inserts a new workspace.
//
// Per FEAT-002's acceptance criteria, this never provisions an engine — engine_status defaults to
// "sleeping" at the database level (migrations/00001_init.sql), and no engine.Provider call happens
// here or anywhere in this handler chain.
func (r *WorkspaceRepository) Create(ctx context.Context, ws domain.Workspace) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, owner_id, engine_status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		ws.ID, ws.Name, ws.OwnerID, string(ws.EngineStatus), ws.CreatedAt, ws.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting workspace: %w", err)
	}
	return nil
}

// ListByOwner returns every workspace owned by ownerID, most recently updated first.
//
// FEAT-045 (read-only sharing, Phase 5) will extend this to also include workspaces the user has
// been invited into via workspace_members; out of scope for Phase 1, so this wave only queries
// ownership.
func (r *WorkspaceRepository) ListByOwner(ctx context.Context, ownerID string) ([]domain.Workspace, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, name, owner_id, engine_status, created_at, updated_at
		 FROM workspaces WHERE owner_id = $1 ORDER BY updated_at DESC`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	defer rows.Close()

	var out []domain.Workspace
	for rows.Next() {
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning workspace: %w", err)
		}
		out = append(out, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workspaces: %w", err)
	}
	return out, nil
}

// CountByOwner reports how many workspaces ownerID currently owns, for the monetization.md tier gate
// (FEAT-002: Free 1, Pro 5, Team unlimited) — enforced server-side, per rule 12.
func (r *WorkspaceRepository) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM workspaces WHERE owner_id = $1`, ownerID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting workspaces: %w", err)
	}
	return count, nil
}

// GetByID returns the workspace with id, or ErrWorkspaceNotFound.
func (r *WorkspaceRepository) GetByID(ctx context.Context, id string) (domain.Workspace, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, name, owner_id, engine_status, created_at, updated_at
		 FROM workspaces WHERE id = $1`,
		id,
	)
	ws, err := scanWorkspace(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workspace{}, ErrWorkspaceNotFound
		}
		return domain.Workspace{}, fmt.Errorf("querying workspace: %w", err)
	}
	return ws, nil
}

// UpdateEngineStatus persists a workspace's live engine status, keeping the `engine_status` column in
// sync with engine.Provider's authoritative in-memory state (FEAT-011).
//
// Deliberately does NOT touch updated_at: screens/03-workspace-list.md sorts by "most recently
// updated," and a workspace's engine idling out in the background is not user activity — bumping
// updated_at here would reorder that list every ~10 minutes for a workspace nobody touched, which is
// exactly the kind of dishonest-feeling behavior FEAT-011 exists to avoid, just inverted (surprising
// the user instead of hiding state from them).
func (r *WorkspaceRepository) UpdateEngineStatus(ctx context.Context, id string, status domain.EngineStatus) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE workspaces SET engine_status = $2 WHERE id = $1`,
		id, string(status),
	)
	if err != nil {
		return fmt.Errorf("updating workspace engine status: %w", err)
	}
	return nil
}

// EngineStatusHook returns an engine.StatusChangeFunc that mirrors every observed engine transition
// into this repository's `engine_status` column via UpdateEngineStatus.
//
// Wire it into engine.NewLocalProvider (or any future Provider implementation using the same hook
// type) via engine.WithStatusChangeHook, so screens that read many workspaces without querying the
// Provider per row — Workspace List's ListByOwner is the current example — reflect real engine state
// rather than staying frozen at whatever engine_status was written as.
//
// Per StatusChangeFunc's own contract, failures here are logged and swallowed rather than propagated:
// a Postgres hiccup persisting a status mirror must never block or fail the engine lifecycle itself,
// and the live GET /v1/workspaces/:id path (api.handleGetWorkspace) queries the Provider directly
// rather than trusting this mirror, so a transient write failure here does not desynchronize the one
// place accuracy is load-bearing.
func (r *WorkspaceRepository) EngineStatusHook(logger *slog.Logger) engine.StatusChangeFunc {
	return func(workspaceID string, status engine.Status) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := r.UpdateEngineStatus(ctx, workspaceID, domain.EngineStatus(status)); err != nil {
			logger.Error("persisting engine status transition",
				slog.String("workspace_id", workspaceID),
				slog.String("status", string(status)),
				slog.String("error", err.Error()),
			)
		}
	}
}

// rowScanner is satisfied by both pgx.Row and pgx.Rows, letting scanWorkspace serve GetByID and
// ListByOwner without duplicating the column list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkspace(row rowScanner) (domain.Workspace, error) {
	var (
		ws     domain.Workspace
		status string
	)
	if err := row.Scan(&ws.ID, &ws.Name, &ws.OwnerID, &status, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
		return domain.Workspace{}, err
	}
	ws.EngineStatus = domain.EngineStatus(status)
	return ws, nil
}
