package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrInvalidCursor is returned by ListFilter.decodeCursor for a cursor that does not parse — a
// tampered or malformed query parameter, never a legitimate pagination state.
var ErrInvalidCursor = errors.New("invalid pagination cursor")

// defaultPageSize matches the "Load more" cursor-pagination contract in screens/17-egress-audit-log.md.
const defaultPageSize = 50

// AuditRepository persists and queries the combined audit/egress log.
type AuditRepository struct {
	pool *Pool
}

// NewAuditRepository constructs an AuditRepository backed by pool. Satisfies audit.Store.
func NewAuditRepository(pool *Pool) *AuditRepository {
	return &AuditRepository{pool: pool}
}

// Create inserts a new audit log entry. WorkspaceID may be empty for account-scoped entries
// (currently only ActionLogin) — stored as SQL NULL per migrations/00002, translated back to "" on
// read so domain.AuditLogEntry keeps schema.md's empty-string-means-absent convention regardless of
// what the database column actually stores it as.
func (r *AuditRepository) Create(ctx context.Context, entry domain.AuditLogEntry) error {
	workspaceID := nullableString(entry.WorkspaceID)

	_, err := r.pool.Exec(ctx,
		`INSERT INTO audit_log_entries (id, workspace_id, user_id, action_type, payload_sent_to_model, timestamp)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		entry.ID, workspaceID, entry.UserID, string(entry.ActionType), entry.PayloadSentToModel, entry.Timestamp,
	)
	if err != nil {
		return fmt.Errorf("inserting audit log entry: %w", err)
	}
	return nil
}

// ListFilter narrows a List query. Every field is optional; the zero value matches everything.
type ListFilter struct {
	// WorkspaceID scopes to one workspace's query_executed/model_call/export/deletion entries.
	WorkspaceID string
	// RequestingUserID scopes the account-level login entries merged in alongside WorkspaceID's
	// workspace-scoped entries — per screens/17's edge case, only the CURRENT user's own logins are
	// ever merged in, never another member's, even for the workspace owner.
	RequestingUserID string
	// ActionType filters to one action type. Empty matches all.
	ActionType string
	// DateFrom/DateTo bound the timestamp range, inclusive. Zero value means unbounded.
	DateFrom time.Time
	DateTo   time.Time
	// Cursor is an opaque pagination token from a previous page's NextCursor. Empty starts at the
	// most recent entry.
	Cursor string
	// PageSize caps the number of entries returned. Defaults to defaultPageSize if zero.
	PageSize int
}

// Page is one page of audit log entries.
type Page struct {
	Entries []domain.AuditLogEntry
	// NextCursor is non-empty when more entries exist beyond this page.
	NextCursor string
}

// List returns entries matching filter, most recent first, per screens/17-egress-audit-log.md's
// data flow: filter changes re-fetch from the first page rather than filtering an unbounded local
// set, and pagination is cursor-based so a high-volume workspace never loads its full history.
func (r *AuditRepository) List(ctx context.Context, filter ListFilter) (Page, error) {
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	var (
		conditions []string
		args       []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	// Merges this workspace's scoped entries with the requesting user's own account-scoped (login)
	// entries in one query, per screens/17: "The endpoint merges both sets server-side so the client
	// never needs a second request or a client-side merge."
	scopeCondition := fmt.Sprintf(
		"(workspace_id = %s OR (workspace_id IS NULL AND user_id = %s))",
		arg(filter.WorkspaceID), arg(filter.RequestingUserID),
	)
	conditions = append(conditions, scopeCondition)

	if filter.ActionType != "" {
		conditions = append(conditions, fmt.Sprintf("action_type = %s", arg(filter.ActionType)))
	}
	if !filter.DateFrom.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= %s", arg(filter.DateFrom)))
	}
	if !filter.DateTo.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp <= %s", arg(filter.DateTo)))
	}

	if filter.Cursor != "" {
		cursorTime, cursorID, err := decodeCursor(filter.Cursor)
		if err != nil {
			return Page{}, err
		}
		// Strict "less than" on the (timestamp, id) pair keeps ties (same-timestamp entries) from
		// being skipped or repeated across pages.
		conditions = append(conditions, fmt.Sprintf(
			"(timestamp, id) < (%s, %s)", arg(cursorTime), arg(cursorID),
		))
	}

	query := fmt.Sprintf(
		`SELECT id, workspace_id, user_id, action_type, payload_sent_to_model, timestamp
		 FROM audit_log_entries
		 WHERE %s
		 ORDER BY timestamp DESC, id DESC
		 LIMIT %s`,
		strings.Join(conditions, " AND "), arg(pageSize+1), // fetch one extra row to detect a next page
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return Page{}, fmt.Errorf("listing audit log entries: %w", err)
	}
	defer rows.Close()

	var entries []domain.AuditLogEntry
	for rows.Next() {
		var (
			entry       domain.AuditLogEntry
			workspaceID *string
			actionType  string
		)
		if err := rows.Scan(&entry.ID, &workspaceID, &entry.UserID, &actionType,
			&entry.PayloadSentToModel, &entry.Timestamp); err != nil {
			return Page{}, fmt.Errorf("scanning audit log entry: %w", err)
		}
		if workspaceID != nil {
			entry.WorkspaceID = *workspaceID
		}
		entry.ActionType = domain.AuditActionType(actionType)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterating audit log entries: %w", err)
	}

	page := Page{Entries: entries}
	if len(entries) > pageSize {
		last := entries[pageSize-1]
		page.Entries = entries[:pageSize]
		page.NextCursor = encodeCursor(last.Timestamp, last.ID)
	}
	return page, nil
}

func encodeCursor(ts time.Time, id string) string {
	raw := ts.Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return ts, parts[1], nil
}

// nullableString maps schema.md's empty-string-means-absent convention onto a SQL NULL for the one
// column (audit_log_entries.workspace_id) where a real foreign key constraint means NULL, not "",
// must carry that meaning at the database level. See migrations/00002_audit_log_account_scope.sql.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DeleteByUser removes every audit/egress entry belonging to userID — FEAT-052's account deletion
// step 5. No audit entry is written for the account deletion itself: every audit row is
// user-FK-cascaded when the users row falls at the final step, so a written entry would be destroyed
// with the account; the per-workspace DeletionRecords from the workspace cascades are the surviving
// proof, and their initiated_by column (plain text, deliberately not FK'd) retains the deleted user
// id as historical reference.
func (r *AuditRepository) DeleteByUser(ctx context.Context, userID string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM audit_log_entries WHERE user_id = $1`,
		userID,
	); err != nil {
		return fmt.Errorf("deleting audit entries for user: %w", err)
	}
	return nil
}
