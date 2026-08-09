// This file implements FEAT-052's account deletion: DELETE /v1/account — the account-scoped sibling
// of FEAT-043's workspace cascade. Order is load-bearing — the `users` row falls LAST, so a failure
// at any step leaves the account intact and retryable (never a false success, mirroring FEAT-043's
// design). Every step is idempotent, which is what makes retry safe.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// Narrow account-cascade seams, satisfied by the postgres repositories. Each is consumer-side, so
// the handler's tests use small fakes without importing the Postgres driver.
type (
	AccountWorkspaceLister interface {
		ListByOwner(ctx context.Context, ownerID string) ([]domain.Workspace, error)
	}
	AccountMemberDeleter interface {
		DeleteByUser(ctx context.Context, userID string) error
	}
	AccountNotificationDeleter interface {
		DeleteByUser(ctx context.Context, userID string) error
	}
	// AccountResetTokenInvalidator destroys every outstanding reset token for a user. The auth
	// package's ResetTokenStore seam already names this InvalidateForUser (the change-password flow
	// uses it too); account deletion needs exactly the same operation.
	AccountResetTokenInvalidator interface {
		InvalidateForUser(ctx context.Context, userID string) error
	}
	AccountAuditDeleter interface {
		DeleteByUser(ctx context.Context, userID string) error
	}
	AccountBillingEventDeleter interface {
		DeleteByUser(ctx context.Context, userID string) error
	}
	AccountUserDeleter interface {
		Delete(ctx context.Context, userID string) error
	}
)

// accountDeletionPartialFailure is the explicit, non-success response for a cascade that did not
// complete: it names the failed step, lists the completed steps, and states plainly that the users
// row was NOT deleted — the account is intact and the deletion can be retried. Never a 200.
type accountDeletionPartialFailure struct {
	Error         string   `json:"error"`
	Code          string   `json:"code"`
	FailedStep    string   `json:"failed_step"`
	Completed     []string `json:"completed_steps"`
	AccountIntact bool     `json:"account_intact"`
}

func respondAccountDeletionPartialFailure(w http.ResponseWriter, failedStep string, completed []string) {
	respondJSON(w, http.StatusInternalServerError, accountDeletionPartialFailure{
		Error: fmt.Sprintf(
			"Your account was not deleted — the %q step of the deletion failed. Your account is intact; retry the deletion.",
			failedStep,
		),
		Code:          "account_deletion_partial_failed",
		FailedStep:    failedStep,
		Completed:     completed,
		AccountIntact: true,
	})
}

// RegisterAccountDeleteRoute wires DELETE /v1/account onto r (mounted inside authsession.RequireAuth
// by the caller — see api.go). The billing argument is the same BillingUserStore the billing routes
// use; billingClient is the same StripeClient.
func RegisterAccountDeleteRoute(
	r chi.Router,
	workspaces AccountWorkspaceLister,
	objects WorkspaceCascadeObjectDeleter,
	engine WorkspaceEngineDropper,
	cascade WorkspaceDeletionStore,
	auditWriter WorkspaceDeletionAuditWriter,
	members AccountMemberDeleter,
	notifications AccountNotificationDeleter,
	resets AccountResetTokenInvalidator,
	audit AccountAuditDeleter,
	billing BillingUserStore,
	billingClient StripeClient,
	billingEvents AccountBillingEventDeleter,
	users AccountUserDeleter,
	logger *slog.Logger,
) {
	r.Delete("/account", handleDeleteAccount(
		workspaces, objects, engine, cascade, auditWriter,
		members, notifications, resets, audit,
		billing, billingClient, billingEvents, users, logger,
	))
}

// handleDeleteAccount serves DELETE /v1/account — FEAT-052's account-scoped cascade. The caller is
// already authenticated (the route sits under RequireAuth); the cascade destroys, in order:
//
//  1. Every OWNED workspace via the full FEAT-043 cascade (each leaving a surviving DeletionRecord).
//     A workspace already deleted by a prior attempt (ErrWorkspaceNotFound) counts as done — the
//     cascade is idempotent and retry-safe.
//  2. The account's read-only memberships in OTHER users' workspaces — those workspaces remain with
//     their owners.
//  3. Notifications, 4. password-reset tokens, 5. audit entries.
//  6. Billing: cancel any active subscription IMMEDIATELY (not at period end), delete the Stripe
//     customer, then delete the account's billing_events rows. No automatic refund.
//  7. LAST — the users row. A failure at any step returns the partial-failure shape with the account
//     intact.
//
// No audit entry is written for the account deletion itself: every audit row is user-FK-cascaded
// when the users row falls at step 7, so a written entry would be destroyed with the account; the
// per-workspace DeletionRecords from step 1 are the surviving, provable trace, and their
// initiated_by column (plain text, deliberately not FK'd) retains the deleted user id as historical
// reference. A stale session cookie from an already-deleted account still decrypts (the JWE is
// self-contained), so every step must tolerate a nonexistent user — DeleteByUser no-ops on zero
// rows, and users.Delete's ErrUserNotFound means the deletion already completed (retry success).
func handleDeleteAccount(
	workspaces AccountWorkspaceLister,
	objects WorkspaceCascadeObjectDeleter,
	engine WorkspaceEngineDropper,
	cascade WorkspaceDeletionStore,
	auditWriter WorkspaceDeletionAuditWriter,
	members AccountMemberDeleter,
	notifications AccountNotificationDeleter,
	resets AccountResetTokenInvalidator,
	audit AccountAuditDeleter,
	billing BillingUserStore,
	billingClient StripeClient,
	billingEvents AccountBillingEventDeleter,
	users AccountUserDeleter,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		completed := []string{}

		// 1. Owned workspaces — the full FEAT-043 cascade per workspace.
		owned, err := workspaces.ListByOwner(ctx, claims.UserID)
		if err != nil {
			logger.Error("account cascade: listing owned workspaces", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		for _, ws := range owned {
			_, wsCompleted, failedStep, err := deleteWorkspaceCascade(ctx, ws.ID, claims.UserID,
				objects, engine, cascade, auditWriter, logger)
			if err != nil {
				if errors.Is(err, postgres.ErrWorkspaceNotFound) {
					// A prior attempt already deleted this workspace — count it done and continue.
					completed = append(completed, "workspace:"+ws.ID)
					continue
				}
				step := fmt.Sprintf("workspace %s (%s)", ws.ID, failedStep)
				respondAccountDeletionPartialFailure(w, step, append(completed, wsCompleted...))
				return
			}
			completed = append(completed, "workspace:"+ws.ID)
		}
		completed = append(completed, "workspaces")

		// 2-5. User-scoped cleanup. Each is a no-op on zero rows, so a stale-cookie retry passes.
		if err := members.DeleteByUser(ctx, claims.UserID); err != nil {
			logger.Error("account cascade: memberships", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondAccountDeletionPartialFailure(w, "memberships", completed)
			return
		}
		completed = append(completed, "memberships")

		if err := notifications.DeleteByUser(ctx, claims.UserID); err != nil {
			logger.Error("account cascade: notifications", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondAccountDeletionPartialFailure(w, "notifications", completed)
			return
		}
		completed = append(completed, "notifications")

		if err := resets.InvalidateForUser(ctx, claims.UserID); err != nil {
			logger.Error("account cascade: reset tokens", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondAccountDeletionPartialFailure(w, "reset_tokens", completed)
			return
		}
		completed = append(completed, "reset_tokens")

		if err := audit.DeleteByUser(ctx, claims.UserID); err != nil {
			logger.Error("account cascade: audit entries", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondAccountDeletionPartialFailure(w, "audit_entries", completed)
			return
		}
		completed = append(completed, "audit_entries")

		// 6. Billing. A Stripe failure ABORTS the cascade (account intact): an account that
		// "deleted" but keeps paying Stripe would be a false success.
		billingState, err := billing.GetBillingByID(ctx, claims.UserID)
		if err != nil {
			if !errors.Is(err, auth.ErrUserNotFound) {
				logger.Error("account cascade: billing lookup", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
				respondAccountDeletionPartialFailure(w, "billing", completed)
				return
			}
		} else {
			if billingState.StripeSubscriptionID != "" {
				if err := billingClient.CancelSubscription(ctx, billingState.StripeSubscriptionID); err != nil {
					logger.Error("account cascade: stripe cancel", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
					respondAccountDeletionPartialFailure(w, "stripe_cancel", completed)
					return
				}
			}
			if billingState.StripeCustomerID != "" {
				if err := billingClient.DeleteCustomer(ctx, billingState.StripeCustomerID); err != nil {
					logger.Error("account cascade: stripe customer delete", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
					respondAccountDeletionPartialFailure(w, "stripe_delete_customer", completed)
					return
				}
			}
		}
		if err := billingEvents.DeleteByUser(ctx, claims.UserID); err != nil {
			logger.Error("account cascade: billing events", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondAccountDeletionPartialFailure(w, "billing_events", completed)
			return
		}
		completed = append(completed, "billing")

		// 7. LAST — the users row. ErrUserNotFound means a prior attempt completed the deletion
		// (only the response was lost); the account is already gone, which is the requested outcome.
		if err := users.Delete(ctx, claims.UserID); err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				respondJSON(w, http.StatusOK, map[string]any{"deleted": true, "completed_steps": completed})
				return
			}
			logger.Error("account cascade: users row", slog.String("user_id", claims.UserID), slog.String("error", err.Error()))
			respondAccountDeletionPartialFailure(w, "account_row", completed)
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"deleted": true, "completed_steps": completed})
	}
}
