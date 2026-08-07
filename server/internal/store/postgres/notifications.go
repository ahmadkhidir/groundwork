package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrNotificationNotFound is returned when no notification matches the given id (or the requesting
// user does not own it — the two are made indistinguishable at the API layer so a non-owner cannot
// probe for the existence of a notification that isn't theirs).
var ErrNotificationNotFound = errors.New("notification not found")

// defaultNotificationPageSize matches the notifications list's pagination contract in
// screens/16-notifications.md: a page of history, with "Load more" appending the next page.
const defaultNotificationPageSize = 50

// NotificationRepository persists and retrieves durable notifications (FEAT-026).
//
// These are deliberately SEPARATE from the live run event stream (architecture.md's "Asynchronous
// (durable notifications)" data flow): the live stream is transient, emitted per step boundary and
// never persisted here, while this repository is the low-frequency, durable channel for export
// ready, engine slept, ingest failed, and workspace invite events.
type NotificationRepository struct {
	pool *Pool
}

// NewNotificationRepository constructs a NotificationRepository backed by pool.
func NewNotificationRepository(pool *Pool) *NotificationRepository {
	return &NotificationRepository{pool: pool}
}

// NotificationPage is one page of a user's notifications.
type NotificationPage struct {
	Notifications []domain.Notification
	// NextCursor is non-empty when more notifications exist beyond this page.
	NextCursor string
}

// Create inserts a new notification for userID. The id and created_at are filled in when the
// caller left them empty, so event-source call sites (export handler, engine status hook, ingest
// failure path) never have to generate either themselves.
//
// Create is best-effort by the CALLER's choice, not by design here: a durable notification that
// fails to persist is a UX gap, never a data-integrity problem, so event sources log and continue
// rather than failing the export/engine-transition/ingest that produced the event.
func (r *NotificationRepository) Create(ctx context.Context, n domain.Notification) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}

	_, err := r.pool.Exec(ctx,
		`INSERT INTO notifications (id, user_id, type, message, related_entity_type, related_entity_id, read, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		n.ID, n.UserID, string(n.Type), n.Message, string(n.RelatedEntityType),
		n.RelatedEntityID, n.Read, n.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting notification: %w", err)
	}
	return nil
}

// ListByUser returns a page of userID's notifications, unread first then read history, each
// partition most recent first, with cursor-based pagination across the combined set (screens/16:
// "unread ones visually distinguished" and FEAT-026's "view a history of past durable
// notifications").
//
// The ordering is the store's contract — the handler never re-sorts — and it is why the (read,
// created_at, id) keyset spans both partitions in one cursor: a page that ends inside the unread
// partition continues there, then crosses into read history on the next page, so a user with more
// unread notifications than one page never loses access to the rest or to their history.
//
// cursor is opaque (from a previous page's NextCursor). An empty cursor starts at the beginning.
// A malformed cursor returns ErrInvalidCursor.
func (r *NotificationRepository) ListByUser(ctx context.Context, userID string, pageSize int, cursor string) (NotificationPage, error) {
	if pageSize <= 0 {
		pageSize = defaultNotificationPageSize
	}

	var c *notificationCursor
	if cursor != "" {
		parsed, err := decodeNotificationCursor(cursor)
		if err != nil {
			return NotificationPage{}, err
		}
		c = &parsed
	}

	// A cursor whose read flag is false is still inside the unread partition (or was placed there by
	// a page that ended exactly at the last unread row); everything else starts in, or continues
	// in, the read partition.
	startingInRead := c != nil && c.read

	if !startingInRead {
		var after *notificationCursor
		if c != nil {
			after = c
		}
		unread, err := r.listPartition(ctx, userID, false, after, pageSize+1)
		if err != nil {
			return NotificationPage{}, err
		}
		if len(unread) > pageSize {
			page := unread[:pageSize]
			return NotificationPage{Notifications: page, NextCursor: encodeNotificationCursor(page[len(page)-1])}, nil
		}

		out := unread
		remaining := pageSize - len(out)
		if remaining == 0 {
			// The page filled exactly with unread rows. There may or may not be more unread after
			// the last one; pointing the cursor at it means the next page asks for unread-after and
			// falls through to read history when there are none — correct either way.
			return NotificationPage{Notifications: out, NextCursor: encodeNotificationCursor(out[len(out)-1])}, nil
		}

		read, err := r.listPartition(ctx, userID, true, nil, remaining+1)
		if err != nil {
			return NotificationPage{}, err
		}
		out = append(out, read...)
		if len(out) > pageSize {
			out = out[:pageSize]
			return NotificationPage{Notifications: out, NextCursor: encodeNotificationCursor(out[len(out)-1])}, nil
		}
		return NotificationPage{Notifications: out}, nil
	}

	read, err := r.listPartition(ctx, userID, true, c, pageSize+1)
	if err != nil {
		return NotificationPage{}, err
	}
	if len(read) > pageSize {
		page := read[:pageSize]
		return NotificationPage{Notifications: page, NextCursor: encodeNotificationCursor(page[len(page)-1])}, nil
	}
	return NotificationPage{Notifications: read}, nil
}

// listPartition fetches up to limit rows from one read/unread partition of userID's notifications,
// most recent first, optionally after the keyset position given by after. limit is fetched as-is:
// callers pass pageSize+1 and truncate, exactly like AuditRepository.List, so a page boundary is
// detected without a second query.
func (r *NotificationRepository) listPartition(ctx context.Context, userID string, read bool, after *notificationCursor, limit int) ([]domain.Notification, error) {
	query := `SELECT id, user_id, type, message, related_entity_type, related_entity_id, read, created_at
		 FROM notifications
		 WHERE user_id = $1 AND read = $2`
	args := []any{userID, read}
	if after != nil {
		args = append(args, after.createdAt, after.id)
		// Strict "less than" on the (created_at, id) pair keeps ties (same-timestamp notifications)
		// from being skipped or repeated across pages.
		query += ` AND (created_at, id) < ($3, $4)`
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}
	defer rows.Close()

	var out []domain.Notification
	for rows.Next() {
		var (
			n           domain.Notification
			ntype       string
			relatedType string
			readFlag    bool
		)
		if err := rows.Scan(&n.ID, &n.UserID, &ntype, &n.Message, &relatedType, &n.RelatedEntityID, &readFlag, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning notification: %w", err)
		}
		n.Type = domain.NotificationType(ntype)
		n.RelatedEntityType = domain.RelatedEntityType(relatedType)
		n.Read = readFlag
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating notifications: %w", err)
	}
	return out, nil
}

// CountUnread reports how many of userID's notifications are still unread — the number the
// navigation-shell bell badge displays (screens/16's Notification bell row).
func (r *NotificationRepository) CountUnread(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND read = FALSE`, userID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting unread notifications: %w", err)
	}
	return count, nil
}

// MarkRead acknowledges one of userID's notifications. Returns ErrNotificationNotFound when no
// notification matches — whether it never existed or belongs to a different user — so the API can
// answer 404 without letting a caller probe for rows that aren't theirs.
func (r *NotificationRepository) MarkRead(ctx context.Context, userID, id string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read = TRUE WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("marking notification read: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationNotFound
	}
	return nil
}

// MarkAllRead acknowledges every unread notification belonging to userID and reports how many rows
// were updated — the "Mark all as read" action on the web page (screens/16).
func (r *NotificationRepository) MarkAllRead(ctx context.Context, userID string) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE notifications SET read = TRUE WHERE user_id = $1 AND read = FALSE`,
		userID,
	)
	if err != nil {
		return 0, fmt.Errorf("marking all notifications read: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ResolveLinks resolves each notification's polymorphic related entity to the workspace that owns
// it, returning a map from notification id to workspace id. The result is the deep-link half the
// client cannot derive from the Notification entity alone — a report or dataset id carries no
// workspace, and a workspace id IS the workspace.
//
// A related entity that has since been deleted (screens/16's edge case: "export ready" for a
// report that was later removed) is absent from the map, so the client can render the row as a
// historical record with its link disabled rather than navigating to a broken screen.
func (r *NotificationRepository) ResolveLinks(ctx context.Context, notifications []domain.Notification) (map[string]string, error) {
	out := make(map[string]string, len(notifications))
	if len(notifications) == 0 {
		return out, nil
	}

	var reportIDs, datasetIDs []string
	for _, n := range notifications {
		switch n.RelatedEntityType {
		case domain.RelatedWorkspace:
			// The related_entity_id of a workspace notification IS the workspace id.
			out[n.ID] = n.RelatedEntityID
		case domain.RelatedReport:
			reportIDs = append(reportIDs, n.RelatedEntityID)
		case domain.RelatedDataset:
			datasetIDs = append(datasetIDs, n.RelatedEntityID)
		}
	}

	workspaceByEntity := make(map[string]string, len(reportIDs)+len(datasetIDs))
	for _, entityType := range []struct {
		ids   []string
		table string
	}{{reportIDs, "reports"}, {datasetIDs, "datasets"}} {
		if len(entityType.ids) == 0 {
			continue
		}
		rows, err := r.pool.Query(ctx,
			`SELECT id, workspace_id FROM `+entityType.table+` WHERE id = ANY($1)`,
			entityType.ids,
		)
		if err != nil {
			return nil, fmt.Errorf("resolving %s links: %w", entityType.table, err)
		}
		for rows.Next() {
			var id, workspaceID string
			if err := rows.Scan(&id, &workspaceID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning %s link: %w", entityType.table, err)
			}
			workspaceByEntity[id] = workspaceID
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterating %s links: %w", entityType.table, err)
		}
		rows.Close()
	}

	for _, n := range notifications {
		if ws, ok := workspaceByEntity[n.RelatedEntityID]; ok {
			out[n.ID] = ws
		}
	}
	return out, nil
}

// notificationCursor is the keyset position of one notification row: its read flag (which partition
// it lives in), its creation timestamp, and its id. Encoding all three makes the cursor span the
// unread-then-read ordering, not just one partition.
type notificationCursor struct {
	read      bool
	createdAt time.Time
	id        string
}

func encodeNotificationCursor(n domain.Notification) string {
	raw := fmt.Sprintf("%t|%s|%s", n.Read, n.CreatedAt.Format(time.RFC3339Nano), n.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeNotificationCursor(cursor string) (notificationCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return notificationCursor{}, ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return notificationCursor{}, ErrInvalidCursor
	}
	read, err := strconv.ParseBool(parts[0])
	if err != nil {
		return notificationCursor{}, ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return notificationCursor{}, ErrInvalidCursor
	}
	if parts[2] == "" {
		return notificationCursor{}, ErrInvalidCursor
	}
	return notificationCursor{read: read, createdAt: ts, id: parts[2]}, nil
}
