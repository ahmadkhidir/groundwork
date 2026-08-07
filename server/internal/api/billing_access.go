package api

import (
	"net/http"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ownerMutationAllowedByTier enforces monetization.md's cancellation/downgrade semantics: when a
// user's tier allows fewer workspaces than they own, the EXCESS workspaces become read-only (not
// deleted) until the user upgrades again or deletes them via the one-workspace-one-delete cascade
// (FEAT-043). Which workspace stays writable is unspecified by the docs; the reasonable default
// implemented here is the MOST RECENTLY UPDATED workspace, which is exactly the workspace list's
// sort order (postgres.WorkspaceRepository.ListByOwner: ORDER BY updated_at DESC), so the writable
// workspace is the one a user already sees first.
//
// This helper is the tier-aware gate the orchestrator inserts into requireWorkspaceMutationAccess
// (members.go) during assembly: an owner's mutation is allowed only when it passes here too. Read
// access is NOT gated — an excess workspace stays fully viewable, only mutation is denied (403
// workspace_read_only_downgraded). Workspace DELETION deliberately stays owner-only via
// requireWorkspaceOwnership (workspace_delete.go), because deletion is the documented escape hatch
// for an excess workspace — gating it would trap the user in the state the gate creates.
//
// Insertion (applied by the orchestrator, not edited here — members.go is Phase 5 shared code):
//
//	func requireWorkspaceMutationAccess(w http.ResponseWriter, r *http.Request,
//		workspaces WorkspaceStore, members MemberLookupStore, tierLookup UserTierLookup,
//		userID, workspaceID string) (domain.Workspace, bool) {
//		ws, role, ok := requireWorkspaceAccess(w, r, workspaces, members, userID, workspaceID)
//		if !ok {
//			return domain.Workspace{}, false
//		}
//		if role == domain.RoleOwner {
//			if !ownerMutationAllowedByTier(w, r, workspaces, tierLookup, ws, userID) {
//				return domain.Workspace{}, false
//			}
//			return ws, true
//		}
//		...
//
// Every existing mutation call site then passes the tier lookup (the same UserTierLookup instance
// the routes already receive) as the new parameter. engine_wake.go's wake-on-intent currently uses
// requireWorkspaceOwnership and SHOULD be swapped to the gated helper too: waking an engine spends
// metered engine-seconds on a workspace the owner cannot mutate. workspace_delete.go must NOT be
// swapped.
func ownerMutationAllowedByTier(w http.ResponseWriter, r *http.Request, workspaces WorkspaceStore, tierLookup UserTierLookup, ws domain.Workspace, userID string) bool {
	if tierLookup == nil {
		// No tier seam wired (e.g. a test server with a zero-value Dependencies) — the ownership
		// check that preceded us is the whole gate. Same graceful degradation as every nil-store guard.
		return true
	}

	user, _, err := tierLookup.FindByID(r.Context(), userID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return false
	}

	limit, ok := workspaceLimits[user.Tier]
	if !ok {
		limit = workspaceLimits[domain.TierFree]
	}
	// Unlimited tier (Team): no excess-workspace condition can exist.
	if limit < 0 {
		return true
	}

	owned, err := workspaces.ListByOwner(r.Context(), userID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return false
	}

	// At or under the tier's workspace ceiling: every workspace is writable.
	if len(owned) <= limit {
		return true
	}

	// Over the ceiling (a downgrade left excess workspaces behind): only the most recently updated
	// workspace stays writable.
	if ws.ID == mostRecentlyUpdatedWorkspace(owned) {
		return true
	}

	respondJSON(w, http.StatusForbidden, map[string]string{
		"error": "This workspace is beyond your plan's workspace limit and is now read-only. Upgrade your plan to keep editing it, or delete excess workspaces.",
		"code":  "workspace_read_only_downgraded",
	})
	return false
}

// mostRecentlyUpdatedWorkspace returns the id of the newest workspace by UpdatedAt — the tiebreak
// that decides which workspace stays writable for an over-limit owner. Mirrors the workspace list's
// ORDER BY updated_at DESC, so the writable workspace is the one already shown first.
func mostRecentlyUpdatedWorkspace(workspaces []domain.Workspace) string {
	var bestID string
	var bestTime time.Time
	for _, ws := range workspaces {
		if bestID == "" || ws.UpdatedAt.After(bestTime) {
			bestID = ws.ID
			bestTime = ws.UpdatedAt
		}
	}
	return bestID
}
