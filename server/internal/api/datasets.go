// This file implements FEAT-048 (Single-Dataset Deletion): screens/04-workspace-home.md's Datasets
// panel (list + per-row delete) and the deletion-impact lookup the shared Delete Confirmation Modal
// needs before the user commits, per that screen's edge cases.
//
// A dataset's deletion is also the mechanism FEAT-049's Duplicate Filename dialog's "Replace" choice
// uses — see upload.go's handleCheckFilename and DeleteDatasetAndObjects below, which upload.go calls
// directly rather than round-tripping through this package's own HTTP handler.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/storage"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// DatasetsStore is the persistence seam RegisterDatasetRoutes depends on for listing, reading, and
// deleting datasets, satisfied by internal/store/postgres.DatasetRepository. A separate, narrow
// interface from DatasetStore (upload.go, Create-only), ProfilingDatasetStore (GetByID-only), and
// ConfirmationDatasetStore (GetByID + ListByWorkspace) for the same reason those are kept distinct
// from each other — see api.go's Dependencies doc comment.
type DatasetsStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
	GetByID(ctx context.Context, id string) (domain.Dataset, error)
	DeleteByID(ctx context.Context, id string) error
}

// DatasetColumnsStore is the persistence seam for reading a dataset's columns, satisfied by
// internal/store/postgres.ColumnRepository. Used to compute FEAT-048's PII-review disclosure.
type DatasetColumnsStore interface {
	ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error)
}

// DatasetRelationshipsStore is the persistence seam for finding semantic-layer relationships that
// depend on a dataset's columns, satisfied by internal/store/postgres.RelationshipRepository.
type DatasetRelationshipsStore interface {
	ListByDatasetColumns(ctx context.Context, workspaceID, datasetID string) ([]postgres.DatasetRelationship, error)
}

// DatasetObjectDeleter is the persistence seam for removing a dataset's objects from object storage,
// satisfied by internal/storage.Client. Deliberately separate from ObjectStore (upload.go,
// PutObject-only): object storage has no foreign keys and does not cascade on its own, so deletion is
// an explicit, narrow capability rather than an assumed side effect of the PutObject seam.
type DatasetObjectDeleter interface {
	DeleteObject(ctx context.Context, key string) error
}

// DatasetDeletionAuditWriter is the seam FEAT-048's delete handler uses to record the deletion,
// satisfied by internal/audit.Writer.
type DatasetDeletionAuditWriter interface {
	WriteDeletion(ctx context.Context, workspaceID, userID string) error
}

// RegisterDatasetRoutes wires FEAT-048's endpoints onto r: listing a workspace's datasets (the
// Datasets panel's data source), reading a dataset's deletion impact (what the Delete Confirmation
// Modal discloses), and deleting a dataset.
//
// members backs the FEAT-045 access check: the list and deletion-impact reads use
// requireWorkspaceAccess (owner OR read-only member — a read-only member may view the datasets
// panel and its impact disclosure), while the DELETE uses requireWorkspaceMutationAccess (only the
// owner may delete).
func RegisterDatasetRoutes(
	r chi.Router,
	workspaces WorkspaceStore,
	members MemberLookupStore,
	datasets DatasetsStore,
	columns DatasetColumnsStore,
	relationships DatasetRelationshipsStore,
	objects DatasetObjectDeleter,
	auditWriter DatasetDeletionAuditWriter,
	logger *slog.Logger,
) {
	r.Get("/workspaces/{workspaceId}/datasets", handleListDatasets(workspaces, members, datasets, logger))
	r.Get("/workspaces/{workspaceId}/datasets/{datasetId}/deletion-impact",
		handleDatasetDeletionImpact(workspaces, members, datasets, columns, relationships, logger))
	r.Delete("/workspaces/{workspaceId}/datasets/{datasetId}",
		handleDeleteDataset(workspaces, members, datasets, objects, auditWriter, logger))
}

// publicDatasetSummary is one row of screens/04-workspace-home.md's Datasets panel.
type publicDatasetSummary struct {
	ID               string `json:"id"`
	OriginalFilename string `json:"original_filename"`
	IngestStatus     string `json:"ingest_status"`
	RowCount         int64  `json:"row_count"`
	CreatedAt        string `json:"created_at"`
}

func toPublicDatasetSummary(ds domain.Dataset) publicDatasetSummary {
	return publicDatasetSummary{
		ID:               ds.ID,
		OriginalFilename: ds.OriginalFilename,
		IngestStatus:     string(ds.IngestStatus),
		RowCount:         ds.RowCount,
		CreatedAt:        ds.CreatedAt.Format(time.RFC3339),
	}
}

// handleListDatasets serves GET /v1/workspaces/:id/datasets — screens/04-workspace-home.md's Datasets
// panel data source. Two datasets sharing an original_filename (FEAT-049's "Keep both" outcome) are
// left in created_at order (most recent first, matching DatasetRepository.ListByWorkspace) so the
// panel can disambiguate them by upload time, per that screen's spec.
func handleListDatasets(workspaces WorkspaceStore, members MemberLookupStore, datasets DatasetsStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}
		found, err := datasets.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing datasets", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicDatasetSummary, 0, len(found))
		for _, ds := range found {
			out = append(out, toPublicDatasetSummary(ds))
		}
		respondJSON(w, http.StatusOK, map[string]any{"datasets": out})
	}
}

// publicDependentRelationship names a semantic-layer relationship at risk from a dataset deletion —
// FEAT-048's requirement that the confirmation "state this before the user commits, rather than
// silently orphaning a semantic-layer entry" needs the relationship named, not merely counted.
type publicDependentRelationship struct {
	ID               string `json:"id"`
	SourceColumnName string `json:"source_column_name"`
	TargetColumnName string `json:"target_column_name"`
}

// datasetDeletionImpact is the shared Delete Confirmation Modal's data source for a single dataset —
// screens.md's "explicit scope of what will be destroyed spelled out in the body," never a bare "are
// you sure?".
type datasetDeletionImpact struct {
	Dataset                publicDatasetSummary          `json:"dataset"`
	ColumnCount            int                           `json:"column_count"`
	DependentRelationships []publicDependentRelationship `json:"dependent_relationships"`
	// HasReviewedPII is a precise, computed signal: true when any column on this dataset has an
	// explicit pii_reviewed_at, per FEAT-007. See HasTypeOverrideRisk's doc comment for why the other
	// half of FEAT-048's disclosure requirement (type overrides) cannot be computed the same way.
	HasReviewedPII bool `json:"has_reviewed_pii"`
	// HasTypeOverrideRisk is true whenever the dataset has any profiled columns at all — NOT a
	// precise "this column was actually overridden" signal.
	//
	// schema.md's Column has no field distinguishing "inferred_type as originally profiled" from
	// "inferred_type after a FEAT-006 override" — UpdateInferredType (columns.go) overwrites the same
	// column in place. That makes an exact answer to FEAT-048's acceptance criterion ("if the dataset
	// has any columns carrying a type override... states that this review work will be permanently
	// lost") impossible to compute from current schema without a migration, which this wave was
	// explicitly told to avoid inventing.
	//
	// Reported rather than guessed at: a false NEGATIVE here (silently omitting the warning when a
	// column really was overridden) would violate the acceptance criterion outright, so this
	// deliberately over-discloses instead — true whenever the dataset has any columns, which is every
	// dataset that reached profiling. The alternative (always false) is not a safe default for a
	// warning whose entire purpose is to never be silently skipped. Flagged for product-shaper: a
	// `Column.type_overridden_at` (paired with `pii_reviewed_at`'s own convention) would make this
	// exact instead of conservative.
	HasTypeOverrideRisk bool `json:"has_type_override_risk"`
}

// handleDatasetDeletionImpact serves GET /v1/workspaces/:id/datasets/:datasetId/deletion-impact — the
// disclosure the shared Delete Confirmation Modal renders before FEAT-048's delete, or before
// FEAT-049's Replace, can be confirmed.
func handleDatasetDeletionImpact(
	workspaces WorkspaceStore,
	members MemberLookupStore,
	datasets DatasetsStore,
	columns DatasetColumnsStore,
	relationships DatasetRelationshipsStore,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		datasetID := chi.URLParam(r, "datasetId")
		ds, ok := loadWorkspaceDataset(w, r, datasets, workspaceID, datasetID, logger)
		if !ok {
			return
		}

		cols, err := columns.ListByDataset(r.Context(), datasetID)
		if err != nil {
			logger.Error("listing columns for deletion impact", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		hasReviewedPII := false
		for _, c := range cols {
			if c.PIIReviewedAt != "" {
				hasReviewedPII = true
				break
			}
		}

		rels, err := relationships.ListByDatasetColumns(r.Context(), workspaceID, datasetID)
		if err != nil {
			logger.Error("listing dependent relationships", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		depRels := make([]publicDependentRelationship, 0, len(rels))
		for _, rel := range rels {
			depRels = append(depRels, publicDependentRelationship{
				ID:               rel.ID,
				SourceColumnName: rel.SourceColumnName,
				TargetColumnName: rel.TargetColumnName,
			})
		}

		respondJSON(w, http.StatusOK, datasetDeletionImpact{
			Dataset:                toPublicDatasetSummary(ds),
			ColumnCount:            len(cols),
			DependentRelationships: depRels,
			HasReviewedPII:         hasReviewedPII,
			HasTypeOverrideRisk:    len(cols) > 0,
		})
	}
}

// handleDeleteDataset serves DELETE /v1/workspaces/:id/datasets/:datasetId — FEAT-048's actual
// deletion. Also called indirectly (as DeleteDatasetAndObjects, not via HTTP) by FEAT-049's Replace
// path in upload.go.
func handleDeleteDataset(
	workspaces WorkspaceStore,
	members MemberLookupStore,
	datasets DatasetsStore,
	objects DatasetObjectDeleter,
	auditWriter DatasetDeletionAuditWriter,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		datasetID := chi.URLParam(r, "datasetId")
		ds, ok := loadWorkspaceDataset(w, r, datasets, workspaceID, datasetID, logger)
		if !ok {
			return
		}

		if err := DeleteDatasetAndObjects(r.Context(), ds, datasets, objects, auditWriter, claims.UserID); err != nil {
			logger.Error("deleting dataset", slog.String("dataset_id", datasetID), slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not delete this dataset. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]string{"id": datasetID})
	}
}

// loadWorkspaceDataset fetches datasetID and verifies it belongs to workspaceID, writing the same
// non-enumerating 404 response the rest of this API uses for "not found" and "not yours" alike (see
// requireWorkspaceOwnership's doc comment).
func loadWorkspaceDataset(w http.ResponseWriter, r *http.Request, datasets DatasetsStore, workspaceID, datasetID string, logger *slog.Logger) (domain.Dataset, bool) {
	ds, err := datasets.GetByID(r.Context(), datasetID)
	if err != nil {
		if errors.Is(err, postgres.ErrDatasetNotFound) {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Dataset not found.",
				"code":  "not_found",
			})
			return domain.Dataset{}, false
		}
		logger.Error("getting dataset", slog.String("error", err.Error()))
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
		return domain.Dataset{}, false
	}
	if ds.WorkspaceID != workspaceID {
		respondJSON(w, http.StatusNotFound, map[string]string{
			"error": "Dataset not found.",
			"code":  "not_found",
		})
		return domain.Dataset{}, false
	}
	return ds, true
}

// DeleteDatasetAndObjects performs FEAT-048's whole deletion for ds: object storage first, then the
// database row, then the audit entry — in that order deliberately.
//
// Object storage is deleted BEFORE the database row so that a storage failure leaves the dataset
// intact and retryable, rather than a DB row already gone with orphaned objects and no record of
// which keys they were at. This is the explicit, application-code delete features.md's description
// requires: "object storage has no foreign keys to the application database and does not cascade on
// its own... a dataset deletion that only removes database rows and leaves orphaned objects... would
// quietly break the same provable-deletion promise this feature exists to uphold."
//
// Both the original upload and the normalized Parquet are targeted. The normalized object is only
// attempted when ParquetLocation is set (a dataset that failed before loading completed never had
// one) — DeleteObject is a no-op success against a key that was never written, but there's no reason
// to make that call when the caller already knows it wasn't.
//
// Exported so upload.go's FEAT-049 Replace path can call it directly rather than round-tripping
// through this package's own HTTP handler.
func DeleteDatasetAndObjects(
	ctx context.Context,
	ds domain.Dataset,
	datasets DatasetsStore,
	objects DatasetObjectDeleter,
	auditWriter DatasetDeletionAuditWriter,
	userID string,
) error {
	originalKey := storage.DatasetKey(ds.WorkspaceID, ds.ID, ds.OriginalFilename)
	if err := objects.DeleteObject(ctx, originalKey); err != nil {
		return fmt.Errorf("deleting original object: %w", err)
	}

	if ds.ParquetLocation != "" {
		normalizedKey := storage.NormalizedDatasetKey(ds.WorkspaceID, ds.ID)
		if err := objects.DeleteObject(ctx, normalizedKey); err != nil {
			return fmt.Errorf("deleting normalized object: %w", err)
		}
	}

	if err := datasets.DeleteByID(ctx, ds.ID); err != nil {
		return fmt.Errorf("deleting dataset row: %w", err)
	}

	// A failed audit write returns an error here (surfacing as a 500 to the caller) even though the
	// deletion itself has already succeeded and is not undone — mirroring confirmation.go's PII
	// override write, which treats a missing audit entry for a trust-surface action as a request
	// failure rather than a warning to log and ignore. A dataset deletion this log can't account for
	// is exactly the silent-drift state FEAT-042 exists to prevent.
	if err := auditWriter.WriteDeletion(ctx, ds.WorkspaceID, userID); err != nil {
		return fmt.Errorf("writing deletion audit entry: %w", err)
	}

	return nil
}
