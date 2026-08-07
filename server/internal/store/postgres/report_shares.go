package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrReportShareNotFound is returned when no ACTIVE (non-revoked) share matches the given token, or
// when no share matches the given id within a workspace.
var ErrReportShareNotFound = errors.New("report share not found")

// ReportShareWithWorkspace is a share row enriched with its denormalized workspace_id.
//
// schema.md's ReportShare entity has no workspace_id property — exactly like Column — but the
// report_shares table carries it (migration 00001) so the workspace-scoped list and the deletion
// cascade (FEAT-043) work without a join through reports. The store returns it alongside the entity
// where a caller needs it (the public token read), never inventing a domain field.
type ReportShareWithWorkspace struct {
	Share       domain.ReportShare
	WorkspaceID string
}

// ReportShareRepository persists read-only report share links (FEAT-046): a token-scoped grant that
// lets anyone holding the token view one report without a workspace invite. Access is scoped to
// possession of the token, not account identity.
type ReportShareRepository struct {
	pool *Pool
}

// NewReportShareRepository constructs a ReportShareRepository backed by pool.
func NewReportShareRepository(pool *Pool) *ReportShareRepository {
	return &ReportShareRepository{pool: pool}
}

// Create inserts a new share row. workspaceID is a separate parameter rather than a field on
// domain.ReportShare for the same reason ColumnRepository.BulkInsert takes workspaceID separately:
// the domain entity (schema.md's mirror) has no workspace_id property; the database column is a
// storage-layer denormalization. The share_token must be generated from a cryptographically secure
// source by the caller — it is the only thing standing between a URL and the report's contents.
func (r *ReportShareRepository) Create(ctx context.Context, workspaceID string, share domain.ReportShare) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO report_shares (id, report_id, workspace_id, share_token, revoked, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		share.ID, share.ReportID, workspaceID, share.ShareToken, share.Revoked, share.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting report share: %w", err)
	}
	return nil
}

// ListActiveByWorkspace returns every non-revoked share for workspaceID, most recently created
// first — screens/13-workspace-settings.md's "Shared report links" section.
func (r *ReportShareRepository) ListActiveByWorkspace(ctx context.Context, workspaceID string) ([]domain.ReportShare, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, report_id, share_token, revoked, created_at
		 FROM report_shares WHERE workspace_id = $1 AND revoked = FALSE
		 ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing report shares: %w", err)
	}
	defer rows.Close()

	var out []domain.ReportShare
	for rows.Next() {
		var share domain.ReportShare
		if err := rows.Scan(&share.ID, &share.ReportID, &share.ShareToken, &share.Revoked, &share.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning report share: %w", err)
		}
		out = append(out, share)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating report shares: %w", err)
	}
	return out, nil
}

// GetActiveByToken returns the non-revoked share whose token matches, plus its workspace id, or
// ErrReportShareNotFound. Revoking a share makes this read fail, which is exactly how "revoking
// invalidates the link" is enforced: a revoked token can no longer resolve to its report.
func (r *ReportShareRepository) GetActiveByToken(ctx context.Context, token string) (ReportShareWithWorkspace, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, report_id, workspace_id, share_token, revoked, created_at
		 FROM report_shares WHERE share_token = $1 AND revoked = FALSE`,
		token,
	)
	return scanReportShareWithWorkspace(row)
}

// Revoke marks the share with id within workspaceID revoked. Returns ErrReportShareNotFound when no
// such row exists (or it belongs to a different workspace). The row is kept, not deleted: schema.md
// models revocation as a state on the share, and keeping the row means a revoked token
// deterministically reads as not-found rather than as a fresh-sounding error.
func (r *ReportShareRepository) Revoke(ctx context.Context, id, workspaceID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE report_shares SET revoked = TRUE WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("revoking report share: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrReportShareNotFound
	}
	return nil
}

// scanReportShareWithWorkspace decodes the six-column share row (including the denormalized
// workspace_id) into the store-side composite.
func scanReportShareWithWorkspace(row rowScanner) (ReportShareWithWorkspace, error) {
	var (
		share       domain.ReportShare
		workspaceID string
	)
	if err := row.Scan(&share.ID, &share.ReportID, &workspaceID, &share.ShareToken, &share.Revoked, &share.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReportShareWithWorkspace{}, ErrReportShareNotFound
		}
		return ReportShareWithWorkspace{}, fmt.Errorf("scanning report share: %w", err)
	}
	return ReportShareWithWorkspace{Share: share, WorkspaceID: workspaceID}, nil
}
