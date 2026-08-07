package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// maxNotificationPageSize clamps the notifications list's ?limit= parameter, mirroring the audit
// log's own clamp so a client bug cannot request an unbounded page of history.
const maxNotificationPageSize = 100

// NotificationStore is the persistence seam RegisterNotificationRoutes depends on, satisfied by
// internal/store/postgres.NotificationRepository.
//
// Create is deliberately NOT part of this seam: the API only reads and acknowledges. Creation
// belongs to the event-source call sites (export handler, engine status hook, ingest failure path)
// via the concrete repository, which the orchestrator wires in main.go.
type NotificationStore interface {
	ListByUser(ctx context.Context, userID string, pageSize int, cursor string) (postgres.NotificationPage, error)
	CountUnread(ctx context.Context, userID string) (int, error)
	MarkRead(ctx context.Context, userID, id string) error
	MarkAllRead(ctx context.Context, userID string) (int, error)
	ResolveLinks(ctx context.Context, notifications []domain.Notification) (map[string]string, error)
}

// RegisterNotificationRoutes wires FEAT-026's endpoints onto r (screens/16-notifications.md),
// mounted under the authenticated group by the caller (see api.go):
//
//	GET   /notifications            — list (unread first), or unread_count=true for the bell
//	PATCH /notifications/{id}       — acknowledge (mark read)
//	POST  /notifications/read-all   — acknowledge everything
//
// users enforces monetization.md row 109 (FEAT-026: Pro/Team only). Every handler re-reads the
// authoritative tier from Postgres rather than trusting the session cookie's tier claim, matching
// every other gated endpoint in this codebase.
func RegisterNotificationRoutes(r chi.Router, notifications NotificationStore, users UserTierLookup, logger *slog.Logger) {
	r.Get("/notifications", handleListNotifications(notifications, users, logger))
	r.Patch("/notifications/{notificationId}", handleAcknowledgeNotification(notifications, users, logger))
	r.Post("/notifications/read-all", handleMarkAllNotificationsRead(notifications, users, logger))
}

// publicNotification is the wire shape of one durable notification: the documented Notification
// entity plus the resolved workspace the client needs to build a deep link (screens/16 Data
// Requirements). WorkspaceID is omitted when the linked entity has been deleted since the
// notification was created — the row stays (it is a historical record) but the link is disabled.
type publicNotification struct {
	ID                string `json:"id"`
	UserID            string `json:"user_id"`
	Type              string `json:"type"`
	Message           string `json:"message"`
	RelatedEntityType string `json:"related_entity_type"`
	RelatedEntityID   string `json:"related_entity_id"`
	Read              bool   `json:"read"`
	CreatedAt         string `json:"created_at"`
	// WorkspaceID is present only when the notification's related entity still exists. The client
	// uses it with RelatedEntityType/RelatedEntityID to build the deep link.
	WorkspaceID string `json:"workspace_id,omitempty"`
}

func toPublicNotification(n domain.Notification, workspaceID string) publicNotification {
	return publicNotification{
		ID:                n.ID,
		UserID:            n.UserID,
		Type:              string(n.Type),
		Message:           n.Message,
		RelatedEntityType: string(n.RelatedEntityType),
		RelatedEntityID:   n.RelatedEntityID,
		Read:              n.Read,
		CreatedAt:         n.CreatedAt.Format(time.RFC3339),
		WorkspaceID:       workspaceID,
	}
}

// requireNotificationTier rejects Free-tier users with 403 upgrade_required, per monetization.md
// row 109 (FEAT-026 is Pro/Team only). The authoritative tier comes from UserTierLookup — the same
// fail-closed discipline as the export tier gate: a gate that could be skipped is not a gate.
func requireNotificationTier(w http.ResponseWriter, r *http.Request, users UserTierLookup, userID string, logger *slog.Logger) bool {
	user, _, err := users.FindByID(r.Context(), userID)
	if err != nil {
		logger.Error("looking up user for notifications tier gate",
			slog.String("user_id", userID),
			slog.String("error", err.Error()),
		)
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return false
	}
	if user.Tier == domain.TierFree {
		respondJSON(w, http.StatusForbidden, map[string]string{
			"error": "Durable notifications are available on the Pro and Team plans. Upgrade to see notifications.",
			"code":  "upgrade_required",
		})
		return false
	}
	return true
}

// handleListNotifications serves GET /api/notifications.
//
// With ?unread_count=true it is the lightweight count-only variant the navigation-shell bell polls
// (screens/16's Notification bell row: "GET /api/notifications?unread_count=true") — no list is
// fetched, only the COUNT on the user's unread rows.
//
// Otherwise it returns one page: unread first, then read history, each most recent first, with
// unread_count and next_cursor alongside so the client never needs a second request to render the
// page or drive the bell badge.
func handleListNotifications(notifications NotificationStore, users UserTierLookup, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		if !requireNotificationTier(w, r, users, claims.UserID, logger) {
			return
		}

		if r.URL.Query().Get("unread_count") == "true" {
			count, err := notifications.CountUnread(r.Context(), claims.UserID)
			if err != nil {
				logger.Error("counting unread notifications",
					slog.String("user_id", claims.UserID),
					slog.String("error", err.Error()),
				)
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			respondJSON(w, http.StatusOK, map[string]any{"unread_count": count})
			return
		}

		pageSize := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				pageSize = parsed
			}
		}
		if pageSize > maxNotificationPageSize {
			pageSize = maxNotificationPageSize
		}

		page, err := notifications.ListByUser(r.Context(), claims.UserID, pageSize, r.URL.Query().Get("cursor"))
		if err != nil {
			if errors.Is(err, postgres.ErrInvalidCursor) {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Invalid pagination cursor.",
					"code":  "validation_error",
				})
				return
			}
			logger.Error("listing notifications",
				slog.String("user_id", claims.UserID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		workspaceByNotification, err := notifications.ResolveLinks(r.Context(), page.Notifications)
		if err != nil {
			logger.Error("resolving notification links",
				slog.String("user_id", claims.UserID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		unreadCount, err := notifications.CountUnread(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("counting unread notifications",
				slog.String("user_id", claims.UserID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicNotification, 0, len(page.Notifications))
		for _, n := range page.Notifications {
			out = append(out, toPublicNotification(n, workspaceByNotification[n.ID]))
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"notifications": out,
			"unread_count":  unreadCount,
			"next_cursor":   page.NextCursor,
		})
	}
}

// handleAcknowledgeNotification serves PATCH /api/notifications/:id — the "Mark as read" action
// (screens/16 Components: "Marks a notification read | PATCH /api/notifications/:id").
//
// The body is optional; when present it must be {"read": true}. Unreading (read: false) is
// deliberately unsupported: FEAT-026's channel is durable-until-acknowledged, and once
// acknowledged a notification stays acknowledged.
func handleAcknowledgeNotification(notifications NotificationStore, users UserTierLookup, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		if !requireNotificationTier(w, r, users, claims.UserID, logger) {
			return
		}

		if r.Body != nil {
			var body struct {
				Read *bool `json:"read"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.Read != nil && !*body.Read {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "Only marking a notification as read is supported.",
					"code":  "validation_error",
				})
				return
			}
		}

		notificationID := chi.URLParam(r, "notificationId")
		if err := notifications.MarkRead(r.Context(), claims.UserID, notificationID); err != nil {
			if errors.Is(err, postgres.ErrNotificationNotFound) {
				// "Doesn't exist" and "belongs to someone else" read as one plain 404 — the same
				// non-enumeration stance as every other owned-resource handler.
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Notification not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("marking notification read",
				slog.String("user_id", claims.UserID),
				slog.String("notification_id", notificationID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleMarkAllNotificationsRead serves POST /api/notifications/read-all — the web page's "Mark
// all as read" Ghost button (screens/16 Layout). It acknowledges without navigating anywhere.
func handleMarkAllNotificationsRead(notifications NotificationStore, users UserTierLookup, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		if !requireNotificationTier(w, r, users, claims.UserID, logger) {
			return
		}

		count, err := notifications.MarkAllRead(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("marking all notifications read",
				slog.String("user_id", claims.UserID),
				slog.String("error", err.Error()),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"marked_read": count})
	}
}
