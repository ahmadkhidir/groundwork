package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrMemberNotFound is returned when no membership row matches the given id within a workspace (or
// when the requesting user has no membership in that workspace — the same non-enumeration stance as
// ErrWorkspaceNotFound).
var ErrMemberNotFound = errors.New("member not found")

// MemberWithUser is a workspace membership row enriched with the invited user's email and display
// name, so screens/13-workspace-settings.md's member list can render "who" without the API layer
// issuing one user lookup per row.
//
// It is a store-side composite, not a domain entity: schema.md's WorkspaceMember carries only
// user_id, and the display fields are exactly what a member-list UI needs beyond the entity.
type MemberWithUser struct {
	Member      domain.WorkspaceMember
	Email       string
	DisplayName string
}

// MemberRepository persists workspace memberships (FEAT-045) — the read-only sharing grant that
// lets a workspace owner invite another account to view reports, charts, and provenance without
// mutating the semantic layer.
//
// The workspace_members table (migration 00001) carries a UNIQUE (workspace_id, user_id) constraint
// and its comment states "a second invite updates rather than duplicates" — so Create upserts on
// that key rather than erroring on a re-invite.
type MemberRepository struct {
	pool *Pool
}

// NewMemberRepository constructs a MemberRepository backed by pool.
func NewMemberRepository(pool *Pool) *MemberRepository {
	return &MemberRepository{pool: pool}
}

// Create inserts a membership row, or updates the existing row for the same (workspace, user) pair
// when the owner re-invites (migration 00001's "a second invite updates rather than duplicates").
// Every invite is read_only: schema.md scopes FEAT-045 to a single read-only role, and concurrent
// semantic-layer editing is explicitly out of scope.
func (r *MemberRepository) Create(ctx context.Context, member domain.WorkspaceMember) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, invited_at, accepted_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (workspace_id, user_id)
		 DO UPDATE SET role = EXCLUDED.role, invited_at = EXCLUDED.invited_at, accepted_at = NULL`,
		member.ID, member.WorkspaceID, member.UserID, string(member.Role), member.InvitedAt,
		member.AcceptedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting workspace member: %w", err)
	}
	return nil
}

// ListByWorkspace returns every membership row for workspaceID, most recently invited first.
func (r *MemberRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.WorkspaceMember, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, user_id, role, invited_at, accepted_at
		 FROM workspace_members WHERE workspace_id = $1 ORDER BY invited_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing workspace members: %w", err)
	}
	defer rows.Close()
	return scanMembers(rows)
}

// ListByWorkspaceWithUsers returns every membership row for workspaceID enriched with each member
// user's email and display name — the shape screens/13's member list renders.
func (r *MemberRepository) ListByWorkspaceWithUsers(ctx context.Context, workspaceID string) ([]MemberWithUser, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT m.id, m.workspace_id, m.user_id, m.role, m.invited_at, m.accepted_at,
		        u.email, u.display_name
		 FROM workspace_members m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.workspace_id = $1 ORDER BY m.invited_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing workspace members with users: %w", err)
	}
	defer rows.Close()

	var out []MemberWithUser
	for rows.Next() {
		var (
			mw         MemberWithUser
			role       string
			acceptedAt *time.Time
		)
		if err := rows.Scan(&mw.Member.ID, &mw.Member.WorkspaceID, &mw.Member.UserID, &role,
			&mw.Member.InvitedAt, &acceptedAt, &mw.Email, &mw.DisplayName); err != nil {
			return nil, fmt.Errorf("scanning member with user: %w", err)
		}
		mw.Member.Role = domain.WorkspaceRole(role)
		mw.Member.AcceptedAt = acceptedAt
		out = append(out, mw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating members with users: %w", err)
	}
	return out, nil
}

// GetByUserAndWorkspace returns the membership row granting userID access to workspaceID, or
// ErrMemberNotFound. This is the API layer's read-only-access check: a user with a row here may
// view the workspace even though they are not its owner.
func (r *MemberRepository) GetByUserAndWorkspace(ctx context.Context, userID, workspaceID string) (domain.WorkspaceMember, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, user_id, role, invited_at, accepted_at
		 FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID,
	)
	member, err := scanMember(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, ErrMemberNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("getting workspace member: %w", err)
	}
	return member, nil
}

// Revoke removes the membership row with id within workspaceID — the owner's "remove action" on
// screens/13's member list. Returns ErrMemberNotFound when no such row exists (or it belongs to a
// different workspace), so a revoked-then-revoked id reads as not found rather than success.
func (r *MemberRepository) Revoke(ctx context.Context, id, workspaceID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM workspace_members WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("revoking workspace member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMemberNotFound
	}
	return nil
}

// DeleteByUser removes every membership row where userID is the member — FEAT-052's account
// deletion removes the account's read-only memberships in other users' workspaces WITHOUT touching
// those workspaces (an account deletion never destroys a workspace it does not own).
func (r *MemberRepository) DeleteByUser(ctx context.Context, userID string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM workspace_members WHERE user_id = $1`,
		userID,
	); err != nil {
		return fmt.Errorf("deleting memberships for user: %w", err)
	}
	return nil
}

// scanMember decodes one workspace_members row. rowScanner (defined in workspaces.go) is satisfied
// by both pgx.Row and pgx.Rows, letting this serve the single-row and multi-row reads without
// duplicating the column list.
func scanMember(row rowScanner) (domain.WorkspaceMember, error) {
	var (
		member     domain.WorkspaceMember
		role       string
		acceptedAt *time.Time
	)
	if err := row.Scan(&member.ID, &member.WorkspaceID, &member.UserID, &role, &member.InvitedAt, &acceptedAt); err != nil {
		return domain.WorkspaceMember{}, err
	}
	member.Role = domain.WorkspaceRole(role)
	member.AcceptedAt = acceptedAt
	return member, nil
}

func scanMembers(rows pgx.Rows) ([]domain.WorkspaceMember, error) {
	var out []domain.WorkspaceMember
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning workspace member: %w", err)
		}
		out = append(out, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workspace members: %w", err)
	}
	return out, nil
}
