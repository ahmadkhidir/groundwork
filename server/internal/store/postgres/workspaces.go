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

// DeleteSemanticLayer removes every semantic-layer row for workspaceID — candidate joins,
// relationships, corrections, metric definitions, columns, and datasets — as FEAT-043's
// "semantic layer" cascade category (features.md FEAT-043: "semantic layer rows (metric definitions,
// corrections, relationships, columns, datasets)" plus "profiling statistics", which live on the
// columns and candidate joins deleted here).
//
// The deletes run children-before-parents (candidate joins/relationships before columns, columns
// before datasets) inside ONE transaction so each statement does real work and the migration 00001
// ON DELETE CASCADE network — which deletes rows and deletes the workspace itself — stays the
// safety net, never the primary mechanism. A failed category leaves the workspace row intact, so the
// deletion handler can abort and the user can retry.
func (r *WorkspaceRepository) DeleteSemanticLayer(ctx context.Context, workspaceID string) error {
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		stmts := []string{
			`DELETE FROM candidate_joins WHERE workspace_id = $1`,
			`DELETE FROM relationships WHERE workspace_id = $1`,
			`DELETE FROM corrections WHERE workspace_id = $1`,
			`DELETE FROM metric_definitions WHERE workspace_id = $1`,
			`DELETE FROM columns WHERE workspace_id = $1`,
			`DELETE FROM datasets WHERE workspace_id = $1`,
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt, workspaceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deleting semantic layer rows: %w", err)
	}
	return nil
}

// DeleteReportsAndConversation removes every report-and-conversation row for workspaceID —
// provenance links, report shares, steps, reports, and runs — as FEAT-043's
// "reports_and_conversation" cascade category ("reports + provenance + report_shares + conversation
// history (runs, steps)").
//
// Children-before-parents in one transaction: provenance links and report shares before reports,
// steps and reports before runs. Deleting the runs last also cascade-removes this workspace's
// run_cache rows via run_cache.source_run_id (migration 00008), so the explicit query-caches step
// that follows confirms an already-empty table — both paths are idempotent.
func (r *WorkspaceRepository) DeleteReportsAndConversation(ctx context.Context, workspaceID string) error {
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		stmts := []string{
			`DELETE FROM provenance_links WHERE workspace_id = $1`,
			`DELETE FROM report_shares WHERE workspace_id = $1`,
			`DELETE FROM steps WHERE workspace_id = $1`,
			`DELETE FROM reports WHERE workspace_id = $1`,
			`DELETE FROM runs WHERE workspace_id = $1`,
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt, workspaceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deleting reports and conversation rows: %w", err)
	}
	return nil
}

// DeleteQueryCaches removes every run_cache row for workspaceID — FEAT-043's "query_caches" cascade
// category. In practice this is usually a no-op: the runs delete (DeleteReportsAndConversation)
// already cascade-removed the workspace's cache rows via run_cache.source_run_id's ON DELETE CASCADE
// (migration 00008). The explicit step is still part of the cascade so the "query caches destroyed"
// claim rests on an executed statement, not on an assumption about another step's cascade.
func (r *WorkspaceRepository) DeleteQueryCaches(ctx context.Context, workspaceID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM run_cache WHERE workspace_id = $1`, workspaceID)
	if err != nil {
		return fmt.Errorf("deleting query caches: %w", err)
	}
	return nil
}

// DeleteAuditLog removes every audit_log_entries row for workspaceID — FEAT-043's "logs" cascade
// category. The deletion handler calls this ONLY after writing the cascade's own audit entry (see
// api/workspace_delete.go): order matters, because on success this statement destroys that entry too,
// and the workspace's surviving proof is deletion_records, not the audit log (screens/17: "the audit
// log itself is destroyed in the deletion cascade").
func (r *WorkspaceRepository) DeleteAuditLog(ctx context.Context, workspaceID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM audit_log_entries WHERE workspace_id = $1`, workspaceID)
	if err != nil {
		return fmt.Errorf("deleting audit log rows: %w", err)
	}
	return nil
}

// DeleteWithProof removes the workspace row and writes its deletion proof atomically — the final
// step of FEAT-043's cascade, and the ONLY write path for a DeletionRecord.
//
// Atomicity is load-bearing: a proof row that exists while its workspace still exists would be a
// false deletion record, and a workspace deleted without a proof would be an unprovable deletion.
// Running both in one transaction means a failure of either rolls back the other, so a retry starts
// from a consistent state. The workspace row delete is what triggers migration 00001's ON DELETE
// CASCADE network — the database-enforced safety net that destroys any workspace-keyed row the
// explicit per-category steps above missed.
//
// Returns ErrWorkspaceNotFound when the workspace row is already gone (a concurrent deletion won the
// race); the transaction rolls back, so no proof row is written for a workspace this call did not
// delete. deletion_records has no FK to workspaces (migration 00001's deliberate exception), so the
// proof survives the row it describes.
func (r *WorkspaceRepository) DeleteWithProof(ctx context.Context, workspaceID string, proof domain.DeletionRecord) error {
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		cats := make([]string, len(proof.DeletedCategories))
		for i, c := range proof.DeletedCategories {
			cats[i] = string(c)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO deletion_records (id, workspace_id, initiated_by, deleted_categories, completed_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			proof.ID, proof.WorkspaceID, proof.InitiatedBy, cats, proof.CompletedAt,
		); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrWorkspaceNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrWorkspaceNotFound) {
			return ErrWorkspaceNotFound
		}
		return fmt.Errorf("deleting workspace with proof: %w", err)
	}
	return nil
}

// withTx runs fn inside a transaction, committing on success and rolling back on any error (including
// a sentinel error fn returns deliberately, like ErrWorkspaceNotFound — the deferred Rollback is a
// no-op on an already-committed or already-aborted transaction, and harmless otherwise).
func (r *WorkspaceRepository) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
