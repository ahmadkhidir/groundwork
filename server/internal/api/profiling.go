package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// ProfilingDatasetStore is the seam RegisterProfilingRoutes uses to confirm a dataset belongs to the
// workspace named in the route, satisfied by internal/store/postgres.DatasetRepository.
//
// ListByWorkspace was added for FEAT-008/FEAT-049: handleListCandidateJoins computes the Relationship
// inference skipped notice (screens/07-ingest-confirmation.md's Relationships step) at READ time, by
// grouping the workspace's datasets by original_filename, rather than persisting a "skip" row anywhere
// — no schema.md entity models it, and nothing else needs it stored.
type ProfilingDatasetStore interface {
	GetByID(ctx context.Context, id string) (domain.Dataset, error)
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
}

// ProfilingColumnStore is the persistence seam for reading profiled columns (FEAT-005), satisfied by
// internal/store/postgres.ColumnRepository.
type ProfilingColumnStore interface {
	ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error)
}

// ProfilingCandidateJoinStore is the persistence seam for reading inferred candidate joins
// (FEAT-008), satisfied by internal/store/postgres.CandidateJoinRepository.
type ProfilingCandidateJoinStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.CandidateJoin, error)
}

// RegisterProfilingRoutes wires FEAT-005/FEAT-008's read endpoints onto r: retrieving already-computed
// column statistics and candidate joins, per features.md's acceptance criterion that profiling
// statistics are "stored and retrievable, not recomputed on every use." Neither endpoint triggers any
// computation — both are pure reads served entirely from Postgres.
//
// members backs the FEAT-045 access check: both routes are reads, so each uses requireWorkspaceAccess
// (owner OR read-only member) — read-only members may view profiling data per FEAT-045 AC2.
func RegisterProfilingRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, datasets ProfilingDatasetStore, columns ProfilingColumnStore, joins ProfilingCandidateJoinStore, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/datasets/{datasetId}/columns", handleListColumns(workspaces, members, datasets, columns, logger))
	r.Get("/workspaces/{workspaceId}/candidate-joins", handleListCandidateJoins(workspaces, members, datasets, joins, logger))
}

type publicColumn struct {
	ID             string  `json:"id"`
	DatasetID      string  `json:"dataset_id"`
	Name           string  `json:"name"`
	InferredType   string  `json:"inferred_type"`
	TypeConfidence float64 `json:"type_confidence"`
	// TypeFitRates is the 0-1 fit rate for EVERY candidate type (schema.md Column.type_fit_rates).
	// The confidence badge for whichever type is currently active is a client-side lookup into this
	// map — FEAT-006's "updates in the same interaction" requirement — so it travels with every
	// column the client renders. Empty for columns profiled before migration 00006; clients treat a
	// missing entry as "unknown" and fall back to TypeConfidence, mirroring UpdateInferredType's
	// COALESCE.
	TypeFitRates map[domain.InferredType]float64 `json:"type_fit_rates,omitempty"`
	PIIFlag      bool                            `json:"pii_flag"`
	PIIPattern   string                          `json:"pii_pattern,omitempty"`
	// PIIReviewedAt is RFC 3339, or "" when no user has ever explicitly reviewed this column's PII
	// flag. Per schema.md, that emptiness is what distinguishes a column that was reviewed and
	// cleared from one that was simply never flagged (FEAT-007).
	PIIReviewedAt     string  `json:"pii_reviewed_at,omitempty"`
	ExcludedFromModel bool    `json:"excluded_from_model"`
	NullRate          float64 `json:"null_rate"`
	DistinctCount     int64   `json:"distinct_count"`
}

func toPublicColumn(c domain.Column) publicColumn {
	return publicColumn{
		ID:                c.ID,
		DatasetID:         c.DatasetID,
		Name:              c.Name,
		InferredType:      string(c.InferredType),
		TypeConfidence:    c.TypeConfidence,
		TypeFitRates:      c.TypeFitRates,
		PIIFlag:           c.PIIFlag,
		PIIPattern:        c.PIIPattern,
		PIIReviewedAt:     c.PIIReviewedAt,
		ExcludedFromModel: c.ExcludedFromModel,
		NullRate:          c.NullRate,
		DistinctCount:     c.DistinctCount,
	}
}

// handleListColumns serves FEAT-005's GET /api/workspaces/:id/datasets/:datasetId/columns.
//
// Verifies the dataset actually belongs to the workspace named in the route, in addition to the
// caller owning that workspace — otherwise a valid dataset id from a workspace the caller does not
// own could be probed via a workspace id the caller does own.
func handleListColumns(workspaces WorkspaceStore, members MemberLookupStore, datasets ProfilingDatasetStore, columns ProfilingColumnStore, logger *slog.Logger) http.HandlerFunc {
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
		ds, err := datasets.GetByID(r.Context(), datasetID)
		if err != nil {
			if errors.Is(err, postgres.ErrDatasetNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Dataset not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("getting dataset for column listing", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		if ds.WorkspaceID != workspaceID {
			// Same response as "not found" — confirming the dataset exists in a DIFFERENT workspace
			// the caller does not own would leak information about that other workspace.
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Dataset not found.",
				"code":  "not_found",
			})
			return
		}

		cols, err := columns.ListByDataset(r.Context(), datasetID)
		if err != nil {
			logger.Error("listing columns", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicColumn, 0, len(cols))
		for _, c := range cols {
			out = append(out, toPublicColumn(c))
		}
		respondJSON(w, http.StatusOK, map[string]any{"columns": out})
	}
}

type publicCandidateJoin struct {
	ID              string  `json:"id"`
	SourceColumnID  string  `json:"source_column_id"`
	TargetColumnID  string  `json:"target_column_id"`
	ConfidenceScore float64 `json:"confidence_score"`
	Basis           string  `json:"basis"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
}

func toPublicCandidateJoin(cj domain.CandidateJoin) publicCandidateJoin {
	return publicCandidateJoin{
		ID:              cj.ID,
		SourceColumnID:  cj.SourceColumnID,
		TargetColumnID:  cj.TargetColumnID,
		ConfidenceScore: cj.ConfidenceScore,
		Basis:           cj.Basis,
		Status:          string(cj.Status),
		CreatedAt:       cj.CreatedAt.Format(time.RFC3339),
	}
}

// skippedDatasetRef names one dataset in a Relationship inference skipped notice pair.
type skippedDatasetRef struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

// skippedFilenamePair is one entry in the Relationship inference skipped notice
// (screens/07-ingest-confirmation.md's Relationships step), per FEAT-008/FEAT-049: two or more
// datasets sharing an original_filename are never compared, and that skip must be named rather than
// left as an unexplained absence of candidates.
type skippedFilenamePair struct {
	Filename string              `json:"filename"`
	Datasets []skippedDatasetRef `json:"datasets"`
}

// computeSkippedFilenamePairs groups datasets by original_filename and returns one entry per group of
// 2 or more — the exact set of pairs FEAT-008's exclusion applied to in internal/ingest.Pipeline.
// Computed at read time from the current dataset list rather than persisted anywhere: no schema.md
// entity models a "skip," and grouping the already-fetched list is cheap and always current, unlike a
// stored record that could drift from which datasets actually still exist.
func computeSkippedFilenamePairs(datasets []domain.Dataset) []skippedFilenamePair {
	byFilename := make(map[string][]domain.Dataset)
	var order []string
	for _, ds := range datasets {
		if _, seen := byFilename[ds.OriginalFilename]; !seen {
			order = append(order, ds.OriginalFilename)
		}
		byFilename[ds.OriginalFilename] = append(byFilename[ds.OriginalFilename], ds)
	}

	out := make([]skippedFilenamePair, 0)
	for _, filename := range order {
		group := byFilename[filename]
		if len(group) < 2 {
			continue
		}
		refs := make([]skippedDatasetRef, 0, len(group))
		for _, ds := range group {
			refs = append(refs, skippedDatasetRef{ID: ds.ID, CreatedAt: ds.CreatedAt.Format(time.RFC3339)})
		}
		out = append(out, skippedFilenamePair{Filename: filename, Datasets: refs})
	}
	return out
}

// handleListCandidateJoins serves FEAT-008's GET /api/workspaces/:id/candidate-joins, plus
// FEAT-008/FEAT-049's Relationship inference skipped notice: any 2+ datasets in the workspace sharing
// an original_filename, which the ingest pipeline never compares against each other (see
// internal/ingest.Pipeline.inferRelationshipsAndComplete).
func handleListCandidateJoins(workspaces WorkspaceStore, members MemberLookupStore, datasets ProfilingDatasetStore, joins ProfilingCandidateJoinStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		found, err := joins.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing candidate joins", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		dsList, err := datasets.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing datasets for skipped-pair notice", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicCandidateJoin, 0, len(found))
		for _, cj := range found {
			out = append(out, toPublicCandidateJoin(cj))
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"candidate_joins": out,
			"skipped_pairs":   computeSkippedFilenamePairs(dsList),
		})
	}
}
