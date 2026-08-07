package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// MemberStore is the persistence seam RegisterMemberRoutes depends on, satisfied by
// internal/store/postgres.MemberRepository.
type MemberStore interface {
	Create(ctx context.Context, member domain.WorkspaceMember) error
	// ListByWorkspaceWithUsers returns each membership row enriched with the invited user's email
	// and display name — the shape screens/13's member list renders without per-row user lookups.
	ListByWorkspaceWithUsers(ctx context.Context, workspaceID string) ([]postgres.MemberWithUser, error)
	Revoke(ctx context.Context, id, workspaceID string) error
}

// MemberLookupStore is the narrow read seam the read-only-access helpers need, satisfied by the
// same MemberRepository. It is deliberately separate from MemberStore so a handler that only checks
// access (never manages membership) depends on exactly one method.
type MemberLookupStore interface {
	GetByUserAndWorkspace(ctx context.Context, userID, workspaceID string) (domain.WorkspaceMember, error)
}

// UserByEmailStore resolves an invite target by email, satisfied by
// internal/store/postgres.UserRepository.FindByEmail. Inviting by email is the whole FEAT-045 flow,
// and only the user store can answer "does this email have an account?" — the API layer never reads
// the users table directly.
type UserByEmailStore interface {
	FindByEmail(ctx context.Context, email string) (domain.User, string, error)
}

// RegisterMemberRoutes wires FEAT-045's endpoints onto r: the workspace owner invites an account by
// email as a read_only member, lists current members, and revokes access. Every route requires an
// authenticated session (mounted under authsession.RequireAuth by the caller — see api.go).
func RegisterMemberRoutes(r chi.Router, workspaces WorkspaceStore, members MemberStore, users UserByEmailStore, tierLookup UserTierLookup, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/members", handleListMembers(workspaces, members, logger))
	r.Post("/workspaces/{workspaceId}/members", handleInviteMember(workspaces, members, users, tierLookup, logger))
	r.Delete("/workspaces/{workspaceId}/members/{memberId}", handleRevokeMember(workspaces, members, logger))
}

// publicMember is one workspace membership as screens/13's member list renders it: the schema.md
// WorkspaceMember fields plus the invited user's email and display name. AcceptedAt follows the
// empty-string-means-absent convention ("" while the invitation is pending).
type publicMember struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	InvitedAt   string `json:"invited_at"`
	AcceptedAt  string `json:"accepted_at,omitempty"`
}

func toPublicMember(mw postgres.MemberWithUser) publicMember {
	out := publicMember{
		ID:          mw.Member.ID,
		WorkspaceID: mw.Member.WorkspaceID,
		UserID:      mw.Member.UserID,
		Email:       mw.Email,
		DisplayName: mw.DisplayName,
		Role:        string(mw.Member.Role),
		InvitedAt:   mw.Member.InvitedAt.Format(time.RFC3339),
	}
	if mw.Member.AcceptedAt != nil {
		out.AcceptedAt = mw.Member.AcceptedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// handleListMembers serves screens/13-workspace-settings.md's member list:
// GET /api/workspaces/:id/members. Owner-only: screens/13 hides sharing management entirely for
// non-owners ("Column Visibility, Sharing management, Audit Log, and Danger Zone are hidden entirely
// for non-owners"), enforced here by requireWorkspaceOwnership.
func handleListMembers(workspaces WorkspaceStore, members MemberStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		list, err := members.ListByWorkspaceWithUsers(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing workspace members", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicMember, 0, len(list))
		for _, mw := range list {
			out = append(out, toPublicMember(mw))
		}
		respondJSON(w, http.StatusOK, map[string]any{"members": out})
	}
}

// inviteMemberRequest is the POST body for FEAT-045's invite-by-email.
type inviteMemberRequest struct {
	Email string `json:"email"`
}

// handleInviteMember serves screens/13-workspace-settings.md's invite-by-email:
// POST /api/workspaces/:id/members.
//
// FEAT-045 is Team-tier (monetization.md), and the gate is checked against the AUTHORITATIVE tier
// from UserTierLookup — never the session cookie's tier claim, the same discipline RegisterExportRoutes
// follows. A nonexistent email reads as 404 with a plain message: an invite flow must be able to tell
// the owner "there is no account for that email" (the opposite of workspace non-enumeration, where
// revealing existence would help an attacker — here the owner already knows who they invited).
func handleInviteMember(workspaces WorkspaceStore, members MemberStore, users UserByEmailStore, tierLookup UserTierLookup, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		ws, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID)
		if !ok {
			return
		}

		var req inviteMemberRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		email := strings.TrimSpace(strings.ToLower(req.Email))
		if email == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "An email address is required.",
				"code":  "validation_error",
			})
			return
		}

		// Team tier only (monetization.md FEAT-045: Free No, Pro No, Team Yes). The authoritative
		// tier is read fresh from Postgres — a JWT claim is user-influenceable data once issued.
		if tierLookup != nil {
			owner, _, err := tierLookup.FindByID(r.Context(), claims.UserID)
			if err != nil {
				logger.Error("looking up owner for invite tier gate", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			if owner.Tier != domain.TierTeam {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error": "Workspace sharing is available on the Team plan. Upgrade to invite read-only members.",
					"code":  "upgrade_required",
				})
				return
			}
		}

		invitee, _, err := users.FindByEmail(r.Context(), email)
		if err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "No account was found for that email. The person you invite must have a Groundwork account.",
					"code":  "invitee_not_found",
				})
				return
			}
			logger.Error("looking up invitee by email", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		// The owner's access is implicit in ownership; creating a read_only row for them would both
		// duplicate the workspace_members UNIQUE pair ambiguity and let the owner be "revoked" from
		// their own workspace. Reject instead of fabricating a membership.
		if invitee.ID == ws.OwnerID {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "You already have access to this workspace as its owner.",
				"code":  "validation_error",
			})
			return
		}

		now := time.Now().UTC()
		member := domain.WorkspaceMember{
			ID:          uuid.NewString(),
			WorkspaceID: workspaceID,
			UserID:      invitee.ID,
			Role:        domain.RoleReadOnly,
			InvitedAt:   now,
		}
		// A re-invite of an existing member upserts (migration 00001's "a second invite updates
		// rather than duplicates"), so Create covers both the first invite and the refresh.
		if err := members.Create(r.Context(), member); err != nil {
			logger.Error("creating workspace member", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"member": publicMember{
			ID:          member.ID,
			WorkspaceID: member.WorkspaceID,
			UserID:      member.UserID,
			Email:       invitee.Email,
			DisplayName: invitee.DisplayName,
			Role:        string(member.Role),
			InvitedAt:   member.InvitedAt.Format(time.RFC3339),
		}})
	}
}

// handleRevokeMember serves screens/13-workspace-settings.md's "remove action":
// DELETE /api/workspaces/:id/members/:memberId. Owner-only. A revoked member loses access on their
// next request (every subsequent read goes through requireWorkspaceAccess and finds no row).
func handleRevokeMember(workspaces WorkspaceStore, members MemberStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		memberID := chi.URLParam(r, "memberId")
		if err := members.Revoke(r.Context(), memberID, workspaceID); err != nil {
			if errors.Is(err, postgres.ErrMemberNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Member not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("revoking workspace member", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"revoked": true, "member_id": memberID})
	}
}

// requireWorkspaceAccess is the FEAT-045 read-side access check: the caller may access the workspace
// if they own it OR hold an active read_only membership. It returns the workspace and the caller's
// role in it. A caller with no relationship to the workspace gets the same 404 as a nonexistent
// workspace (non-enumeration).
//
// This is the helper the orchestrator inserts into READ handlers so a read-only member can view
// reports, charts, provenance, the semantic layer, and usage — FEAT-045 AC2's "can view reports,
// charts, and provenance". It is deliberately NOT used by mutating handlers; see
// requireWorkspaceMutationAccess.
func requireWorkspaceAccess(w http.ResponseWriter, r *http.Request, workspaces WorkspaceStore, members MemberLookupStore, userID, workspaceID string) (domain.Workspace, domain.WorkspaceRole, bool) {
	ws, err := workspaces.GetByID(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, postgres.ErrWorkspaceNotFound) {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Workspace not found.",
				"code":  "not_found",
			})
			return domain.Workspace{}, "", false
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return domain.Workspace{}, "", false
	}

	if ws.OwnerID == userID {
		return ws, domain.RoleOwner, true
	}

	if members == nil {
		// No membership seam wired — the only grant that exists is ownership. Same non-enumeration
		// 404 as a non-owner.
		respondJSON(w, http.StatusNotFound, map[string]string{
			"error": "Workspace not found.",
			"code":  "not_found",
		})
		return domain.Workspace{}, "", false
	}

	member, err := members.GetByUserAndWorkspace(r.Context(), userID, workspaceID)
	if err != nil {
		if errors.Is(err, postgres.ErrMemberNotFound) {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Workspace not found.",
				"code":  "not_found",
			})
			return domain.Workspace{}, "", false
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return domain.Workspace{}, "", false
	}

	return ws, member.Role, true
}

// requireWorkspaceMutationAccess is the FEAT-045 mutating-side access check, enforced SERVER-side on
// every mutating request per architecture.md's Authentication & Authorization section ("a read-only
// session cannot mutate the semantic layer or trigger new runs regardless of what the client sends").
//
// An owner passes. A read_only member gets 403 with code "read_only_member" — they ARE authenticated
// and granted access, just not write access, and a distinct 403 (rather than the non-owner 404) lets
// the client show the honest "You have read-only access to this workspace" treatment. A caller with
// no membership gets the same 404 as a nonexistent workspace (non-enumeration).
//
// Mutating handlers today call requireWorkspaceOwnership; the orchestrator swaps those calls to this
// helper during assembly so the guard covers uploads, ingests, column/candidate-join overrides,
// semantic-layer writes, run creation, exports, dataset deletion, and workspace deletion. The helper
// itself is defined here, in this wave's owned file, so the swap is a one-line change per handler.
func requireWorkspaceMutationAccess(w http.ResponseWriter, r *http.Request, workspaces WorkspaceStore, members MemberLookupStore, userID, workspaceID string) (domain.Workspace, bool) {
	ws, role, ok := requireWorkspaceAccess(w, r, workspaces, members, userID, workspaceID)
	if !ok {
		return domain.Workspace{}, false
	}
	if role == domain.RoleOwner {
		return ws, true
	}
	respondJSON(w, http.StatusForbidden, map[string]string{
		"error": "You have read-only access to this workspace and cannot make changes.",
		"code":  "read_only_member",
	})
	return domain.Workspace{}, false
}
