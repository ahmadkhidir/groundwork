// This file implements FEAT-043 (One-Workspace-One-Delete Cascade): screens/14-deletion-confirmation-
// proof.md's single, provable cascade and the deletion-proof read.
//
// One handler drives every category in order — object store contents, semantic layer,
// reports_and_conversation, query caches, logs — and reports per-category completion. The workspace
// row is deleted LAST, atomically with its proof record, so a failure at any category leaves the
// workspace intact and retryable: never a false success, and never a false proof (Flow 5 Alternate
// Path B's "the system does not show a false Deletion Proof").
//
// This is deliberately ONE endpoint, not a series of independently-triggered deletions — features.md
// FEAT-043's acceptance criterion "Deletion is a single cascading operation, not a series of
// independently-triggered deletions". The FK cascade network (migration 00001 rule 2) remains the
// database-enforced safety net behind the explicit per-category steps, not the primary mechanism.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// WorkspaceCascadeObjectDeleter removes every object under a workspace's key prefix from object
// storage, satisfied by internal/storage.Client once it gains DeleteObjectsByPrefix (the exact method
// is reported with this feature — storage.go is a shared module, not edited here). Object storage has
// no foreign keys and does not cascade on its own — confirmed operationally for FEAT-048 — so a
// workspace's objects are removed by an explicit prefix sweep, never assumed to disappear with the
// workspace row.
type WorkspaceCascadeObjectDeleter interface {
	DeleteObjectsByPrefix(ctx context.Context, prefix string) error
}

// WorkspaceEngineDropper tears down a workspace's session-scoped engine state, satisfied by
// engine.Provider once it gains Drop. The LocalProvider keeps one in-memory DuckDB connection per
// workspace; without a drop, a deleted workspace leaves a ghost session behind. Not one of the five
// recorded DeletedCategories — engine state is ephemeral — but still part of the cascade scope
// (features.md FEAT-043's "DuckDB snapshots/engine state"), so its failure aborts the cascade like a
// category failure. May be nil (engine teardown skipped) only until the provider seam exists; see the
// report for the exact Drop method needed.
type WorkspaceEngineDropper interface {
	Drop(workspaceID string) error
}

// WorkspaceDeletionStore is the persistence seam for the cascade's database half, satisfied by
// internal/store/postgres.WorkspaceRepository: each category's rows are deleted explicitly (children
// before parents, so the FK cascade never has to catch up), and DeleteWithProof removes the workspace
// row atomically with its deletion proof.
type WorkspaceDeletionStore interface {
	DeleteSemanticLayer(ctx context.Context, workspaceID string) error
	DeleteReportsAndConversation(ctx context.Context, workspaceID string) error
	DeleteQueryCaches(ctx context.Context, workspaceID string) error
	DeleteAuditLog(ctx context.Context, workspaceID string) error
	DeleteWithProof(ctx context.Context, workspaceID string, proof domain.DeletionRecord) error
}

// DeletionProofStore reads a workspace's deletion proof after the workspace itself is gone, satisfied
// by internal/store/postgres.DeletionRecordRepository. The workspace no longer exists to check
// ownership against, so handlers authorize against the record's initiated_by instead.
type DeletionProofStore interface {
	GetByWorkspace(ctx context.Context, workspaceID string) (domain.DeletionRecord, error)
}

// WorkspaceDeletionAuditWriter records the deletion in the combined audit/egress log, satisfied by
// internal/audit.Writer (the same instance behind FEAT-048's dataset-deletion seam). The entry is
// written BEFORE the cascade destroys the workspace's audit rows — order matters, because once the
// workspace row is gone the log rows (and this entry with them) are cascade-deleted, and on a
// later-stage failure the entry must already exist to account for the deletion attempt.
type WorkspaceDeletionAuditWriter interface {
	WriteDeletion(ctx context.Context, workspaceID, userID string) error
}

// RegisterWorkspaceDeleteRoute wires FEAT-043's endpoints onto r:
//
//   - DELETE /v1/workspaces/{workspaceId} — the cascade itself.
//   - GET /v1/workspaces/{workspaceId}/deletion — the follow-up proof read screens/14 documents
//     ("from the DELETE response or a follow-up GET"), which lets the Deletion Proof page recover the
//     record when the DELETE response was not carried (refresh, lost session storage).
//
// Every route requires an authenticated session (mounted under authsession.RequireAuth by the caller
// — see api.go).
func RegisterWorkspaceDeleteRoute(
	r chi.Router,
	workspaces WorkspaceStore,
	objects WorkspaceCascadeObjectDeleter,
	engine WorkspaceEngineDropper,
	cascade WorkspaceDeletionStore,
	proofs DeletionProofStore,
	auditWriter WorkspaceDeletionAuditWriter,
	logger *slog.Logger,
) {
	r.Delete("/workspaces/{workspaceId}", handleDeleteWorkspace(workspaces, objects, engine, cascade, auditWriter, logger))
	r.Get("/workspaces/{workspaceId}/deletion", handleGetDeletionProof(proofs, logger))
}

// deletionPartialFailure is the explicit, non-success response for a cascade that did not complete:
// it names the failed category, lists the categories already destroyed, and states plainly that the
// workspace row was NOT deleted — the workspace is intact and the deletion can be retried. This is
// Flow 5 Alternate Path B's "never a false success" in wire form, and it is a 500, never a 200.
type deletionPartialFailure struct {
	Error           string   `json:"error"`
	Code            string   `json:"code"`
	FailedCategory  string   `json:"failed_category"`
	Completed       []string `json:"completed_categories"`
	WorkspaceIntact bool     `json:"workspace_intact"`
}

func respondDeletionPartialFailure(w http.ResponseWriter, failedCategory string, completed []string) {
	respondJSON(w, http.StatusInternalServerError, deletionPartialFailure{
		Error: fmt.Sprintf(
			"The workspace was not deleted — the %q stage of the cascade failed. Your workspace is intact; retry the deletion.",
			failedCategory,
		),
		Code:            "deletion_partial_failed",
		FailedCategory:  failedCategory,
		Completed:       completed,
		WorkspaceIntact: true,
	})
}

// handleDeleteWorkspace serves DELETE /v1/workspaces/{workspaceId} — FEAT-043's single cascading
// operation.
//
// Order is deliberate and load-bearing:
//
//  1. object store contents — the explicit prefix sweep, FEAT-048's lesson at workspace scope.
//  2. engine state — drop any session-scoped engine so no ghost engine outlives its workspace.
//     3-5. the database categories in domain.AllDeletedCategories order (semantic layer,
//     reports_and_conversation, query_caches), each an explicit store call.
//  6. logs — the cascade's own audit entry is written FIRST (the last moment the workspace's audit
//     rows exist to be written into), then the log rows are destroyed.
//  7. LAST — the workspace row, atomically with the proof record. Only now, after every category
//     succeeded, is the workspace actually deleted; migration 00001's ON DELETE CASCADE network
//     destroys any workspace-keyed row the explicit pass missed, and deletion_records survives as the
//     proof.
//
// A failure at any step returns the partial-failed shape with the workspace row untouched, so a
// retry re-runs the cascade from the start — every step is idempotent (deleting already-deleted
// objects/rows succeeds), which is what makes "retry the cascade automatically" (Flow 5 Alternate
// Path B) actually safe.
func handleDeleteWorkspace(
	workspaces WorkspaceStore,
	objects WorkspaceCascadeObjectDeleter,
	engine WorkspaceEngineDropper,
	cascade WorkspaceDeletionStore,
	auditWriter WorkspaceDeletionAuditWriter,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		// Owner-only: screens/14's "deletion is owner-only, enforced at the Workspace Settings level"
		// and architecture.md's API-layer enforcement. A read-only shared member or any non-owner gets
		// the same 404 as a workspace that does not exist — the API's non-enumeration rule.
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		ctx := r.Context()
		completed := []string{}

		// 1. Object store contents.
		if err := objects.DeleteObjectsByPrefix(ctx, "workspaces/"+workspaceID+"/"); err != nil {
			logger.Error("cascade: object store", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, string(domain.CategoryObjectStore), completed)
			return
		}
		completed = append(completed, string(domain.CategoryObjectStore))

		// 2. Engine state (not a recorded category — see WorkspaceEngineDropper's doc comment).
		if engine != nil {
			if err := engine.Drop(workspaceID); err != nil {
				logger.Error("cascade: engine teardown", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
				respondDeletionPartialFailure(w, "engine", completed)
				return
			}
		}

		// 3-5. The database categories, in domain.AllDeletedCategories order so the completed list and
		// the persisted proof match the documented enumeration.
		if err := cascade.DeleteSemanticLayer(ctx, workspaceID); err != nil {
			logger.Error("cascade: semantic layer", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, string(domain.CategorySemanticLayer), completed)
			return
		}
		completed = append(completed, string(domain.CategorySemanticLayer))

		if err := cascade.DeleteReportsAndConversation(ctx, workspaceID); err != nil {
			logger.Error("cascade: reports and conversation", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, string(domain.CategoryReportsAndConversation), completed)
			return
		}
		completed = append(completed, string(domain.CategoryReportsAndConversation))

		if err := cascade.DeleteQueryCaches(ctx, workspaceID); err != nil {
			logger.Error("cascade: query caches", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, string(domain.CategoryQueryCaches), completed)
			return
		}
		completed = append(completed, string(domain.CategoryQueryCaches))

		// 6. Logs — audit entry first, then destruction (order matters; see WorkspaceDeletionAuditWriter).
		if err := auditWriter.WriteDeletion(ctx, workspaceID, claims.UserID); err != nil {
			logger.Error("cascade: audit write", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, string(domain.CategoryLogs), completed)
			return
		}
		if err := cascade.DeleteAuditLog(ctx, workspaceID); err != nil {
			logger.Error("cascade: audit log rows", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, string(domain.CategoryLogs), completed)
			return
		}
		completed = append(completed, string(domain.CategoryLogs))

		// 7. LAST — the workspace row, atomically with the proof.
		proof := domain.DeletionRecord{
			ID:                uuid.NewString(),
			WorkspaceID:       workspaceID,
			InitiatedBy:       claims.UserID,
			DeletedCategories: append([]domain.DeletedCategory(nil), domain.AllDeletedCategories...),
			CompletedAt:       time.Now(),
		}
		if err := cascade.DeleteWithProof(ctx, workspaceID, proof); err != nil {
			if errors.Is(err, postgres.ErrWorkspaceNotFound) {
				// A concurrent deletion won the race: the workspace is gone, so there is nothing left
				// to delete or retry against. The tx rolled back, so no stray proof row was written.
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Workspace not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("cascade: workspace row + proof", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondDeletionPartialFailure(w, "workspace_row", completed)
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"deletion": proof})
	}
}

// handleGetDeletionProof serves GET /v1/workspaces/{workspaceId}/deletion — screens/14's follow-up
// read of a workspace's deletion proof.
//
// The workspace itself is gone, so ownership cannot be checked against it; authorization is against
// the proof record's initiated_by instead. A missing record, or one initiated by someone else, is the
// same non-enumerating 404 — a user can only read proof of a deletion they themselves executed.
func handleGetDeletionProof(proofs DeletionProofStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")

		rec, err := proofs.GetByWorkspace(r.Context(), workspaceID)
		if err != nil {
			if errors.Is(err, postgres.ErrDeletionRecordNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "No deletion record found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("reading deletion proof", slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		if rec.InitiatedBy != claims.UserID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "No deletion record found.",
				"code":  "not_found",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"deletion": rec})
	}
}
