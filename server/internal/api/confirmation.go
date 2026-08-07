// This file implements screens/07-ingest-confirmation.md's write/aggregate endpoints — FEAT-006 (type
// override), FEAT-007 (PII flag override), and FEAT-009 (candidate join accept/reject) — the
// mandatory review gate before any ingested data is queryable. profiling.go's existing read-only
// per-dataset /columns and per-workspace /candidate-joins endpoints are extended here rather than
// duplicated: this file adds a workspace-wide columns read (spanning every dataset in one ingest
// batch, which the confirmation screen reviews as a whole) plus the two override/decision writes.
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
	"github.com/ahmadkhidir/groundwork/server/internal/relationships"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// ConfirmationDatasetStore is the seam confirmation handlers use to resolve a dataset's original
// filename (for the workspace-wide column listing) and row count (for defaulting a confirmed
// relationship's cardinality), satisfied by internal/store/postgres.DatasetRepository.
type ConfirmationDatasetStore interface {
	GetByID(ctx context.Context, id string) (domain.Dataset, error)
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
}

// ConfirmationColumnStore is the persistence seam for reviewing and overriding profiled columns
// (FEAT-006/FEAT-007/FEAT-010), satisfied by internal/store/postgres.ColumnRepository.
type ConfirmationColumnStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Column, error)
	GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Column, error)
	UpdateInferredType(ctx context.Context, id string, t domain.InferredType) error
	UpdatePII(ctx context.Context, id string, flag bool, pattern string, reviewedAt time.Time) error
	UpdateExclusion(ctx context.Context, id string, excluded bool) error
}

// ConfirmationAuditWriter is the seam the PII override handler uses to record FEAT-007's explicit
// user decision in the audit log, satisfied by internal/audit.Writer.
//
// It is a required collaborator of the override route, not an optional extra: schema.md pairs
// Column.pii_reviewed_at with a `pii_override` AuditLogEntry, and IMPLEMENTATION.md rule 7 requires
// a trust-surface write to happen in the same code path as the action it records. A handler that
// could be constructed without an audit writer is a handler that can silently stop producing
// evidence.
type ConfirmationAuditWriter interface {
	WritePIIOverride(ctx context.Context, workspaceID, userID string) error
}

// ConfirmationCandidateJoinStore is the persistence seam for reviewing and deciding candidate joins
// (FEAT-009), satisfied by internal/store/postgres.CandidateJoinRepository.
type ConfirmationCandidateJoinStore interface {
	GetByID(ctx context.Context, id string) (domain.CandidateJoin, error)
	UpdateStatus(ctx context.Context, id string, status domain.CandidateJoinStatus) error
}

// ConfirmationRelationshipStore is the persistence seam for recording an accepted candidate join into
// the semantic layer (FEAT-009's write side of FEAT-008), satisfied by
// internal/store/postgres.RelationshipRepository. Deliberately the ONLY relationship operation wired
// this wave — FEAT-019 (full semantic layer read/write) is Phase 2 and not authorized.
type ConfirmationRelationshipStore interface {
	Create(ctx context.Context, rel domain.Relationship) error
}

// uniqueCardinalityThreshold mirrors relationships.UniqueCardinalityThreshold's role: the uniqueness
// ratio above which a column reads as key-like when defaulting a confirmed Relationship's cardinality.
// Read via the exported constant rather than redeclared, so the one tunable value lives in one place.
const uniqueCardinalityThreshold = relationships.UniqueCardinalityThreshold

// RegisterConfirmationRoutes wires FEAT-006/FEAT-007/FEAT-009's write endpoints onto r, for
// screens/07-ingest-confirmation.md.
//
// members backs the FEAT-045 access check: the columns LIST is a read (requireWorkspaceAccess, so a
// read-only member may view the confirmation surface), while the column/candidate-join PATCHes are
// mutations (requireWorkspaceMutationAccess — a read-only member must not override types, PII flags,
// exclusions, or join status).
func RegisterConfirmationRoutes(
	r chi.Router,
	workspaces WorkspaceStore,
	members MemberLookupStore,
	datasets ConfirmationDatasetStore,
	columns ConfirmationColumnStore,
	joins ConfirmationCandidateJoinStore,
	relationshipStore ConfirmationRelationshipStore,
	auditWriter ConfirmationAuditWriter,
	logger *slog.Logger,
) {
	r.Get("/workspaces/{workspaceId}/columns", handleListWorkspaceColumns(workspaces, members, datasets, columns, logger))
	r.Patch("/workspaces/{workspaceId}/columns/{columnId}", handleUpdateColumn(workspaces, members, columns, auditWriter, logger))
	// FEAT-010's exclusion toggle (screens/13's Column exclusion toggle component names this exact
	// route as its data source). It reuses the SAME handler as the general column PATCH — the request
	// body is identical ({ "excluded_from_model": true }) and the handler applies only the fields the
	// request actually carries — so one code path serves both the review-screen override and the
	// settings-tab toggle instead of drifting into two behaviors.
	r.Patch("/workspaces/{workspaceId}/columns/{columnId}/exclusion", handleUpdateColumn(workspaces, members, columns, auditWriter, logger))
	r.Patch("/workspaces/{workspaceId}/candidate-joins/{joinId}", handleUpdateCandidateJoin(workspaces, members, columns, datasets, joins, relationshipStore, logger))
}

// publicRelationship is a confirmed relationship as returned when a candidate join is accepted.
type publicRelationship struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	SourceColumnID string `json:"source_column_id"`
	TargetColumnID string `json:"target_column_id"`
	Cardinality    string `json:"cardinality"`
	GrainNotes     string `json:"grain_notes,omitempty"`
	ConfirmedAt    string `json:"confirmed_at"`
}

func toPublicRelationship(rel domain.Relationship) publicRelationship {
	return publicRelationship{
		ID:             rel.ID,
		WorkspaceID:    rel.WorkspaceID,
		SourceColumnID: rel.SourceColumnID,
		TargetColumnID: rel.TargetColumnID,
		Cardinality:    string(rel.Cardinality),
		GrainNotes:     rel.GrainNotes,
		ConfirmedAt:    rel.ConfirmedAt.Format(time.RFC3339),
	}
}

// publicConfirmationColumn is one column as shown on screens/07's Type/PII review tables — the
// existing publicColumn (profiling.go) plus the owning dataset's filename AND created_at, since the
// confirmation screen reviews every column across an entire ingest batch (potentially several files)
// at once and MUST group by source dataset, per that screen's hard requirement: two datasets sharing
// an original_filename (FEAT-049's "Keep both" outcome) are indistinguishable by filename alone, and
// a real incident produced exactly that — 4 duplicate datasets' 16 identical columns rendered as an
// undifferentiated, unlabeled repeating list. DatasetCreatedAt is what disambiguates them.
type publicConfirmationColumn struct {
	ID               string  `json:"id"`
	DatasetID        string  `json:"dataset_id"`
	DatasetFilename  string  `json:"dataset_filename"`
	DatasetCreatedAt string  `json:"dataset_created_at"`
	Name             string  `json:"name"`
	InferredType     string  `json:"inferred_type"`
	TypeConfidence   float64 `json:"type_confidence"`
	// TypeFitRates is the 0-1 fit rate for EVERY candidate type, so the Types step's confidence badge
	// can update in the same interaction as an override via a client-side lookup into
	// schema.md Column.type_fit_rates — FEAT-006's contract, and screens/07's stated rendering rule
	// (`type_fit_rates[override ?? inferred_type]`).
	TypeFitRates map[domain.InferredType]float64 `json:"type_fit_rates,omitempty"`
	PIIFlag      bool                            `json:"pii_flag"`
	PIIPattern   string                          `json:"pii_pattern,omitempty"`
	// PIIReviewedAt is RFC 3339, or "" when no user has ever reviewed this column's flag — the
	// distinction FEAT-007 requires between reviewed-and-cleared and never-flagged.
	PIIReviewedAt     string  `json:"pii_reviewed_at,omitempty"`
	ExcludedFromModel bool    `json:"excluded_from_model"`
	NullRate          float64 `json:"null_rate"`
	DistinctCount     int64   `json:"distinct_count"`
}

// handleListWorkspaceColumns serves GET /v1/workspaces/:id/columns — every profiled column across
// every dataset in the workspace, labelled with its owning file's name. This is the "types" and
// "pii-flags" data source for screens/07-ingest-confirmation.md, which reviews an entire upload batch
// (potentially several files) as one screen rather than one dataset at a time.
func handleListWorkspaceColumns(workspaces WorkspaceStore, members MemberLookupStore, datasets ConfirmationDatasetStore, columns ConfirmationColumnStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		dsList, err := datasets.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing datasets for column review", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		filenameByDatasetID := make(map[string]string, len(dsList))
		createdAtByDatasetID := make(map[string]string, len(dsList))
		for _, ds := range dsList {
			filenameByDatasetID[ds.ID] = ds.OriginalFilename
			createdAtByDatasetID[ds.ID] = ds.CreatedAt.Format(time.RFC3339)
		}

		cols, err := columns.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing workspace columns", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		out := make([]publicConfirmationColumn, 0, len(cols))
		for _, c := range cols {
			out = append(out, publicConfirmationColumn{
				ID:                c.ID,
				DatasetID:         c.DatasetID,
				DatasetFilename:   filenameByDatasetID[c.DatasetID],
				DatasetCreatedAt:  createdAtByDatasetID[c.DatasetID],
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
			})
		}
		respondJSON(w, http.StatusOK, map[string]any{"columns": out})
	}
}

// validInferredTypes mirrors schema.md's Column.inferred_type enum, for validating FEAT-006's type
// override request.
var validInferredTypes = map[string]domain.InferredType{
	"string":    domain.TypeString,
	"integer":   domain.TypeInteger,
	"number":    domain.TypeNumber,
	"boolean":   domain.TypeBoolean,
	"date":      domain.TypeDate,
	"timestamp": domain.TypeTimestamp,
}

// updateColumnRequest is the PATCH body for overriding a column's reviewed type, PII flag, and/or
// model exclusion. Every field is a pointer so the handler can tell "not supplied" (nil) apart from
// an explicit false/zero-value — a request overriding only the PII flag must not accidentally reset
// the type or the exclusion, and vice versa.
type updateColumnRequest struct {
	InferredType *string `json:"inferred_type"`
	PIIFlag      *bool   `json:"pii_flag"`
	// PIIPattern is only consulted when PIIFlag is true; if omitted, "manual" is recorded — this is a
	// user-initiated flag, not one DetectAll proposed, per pii.PatternManual's doc comment.
	PIIPattern *string `json:"pii_pattern"`
	// ExcludedFromModel is FEAT-010's user-controlled override on top of automatic PII flagging: the
	// column stays queryable by the engine but its name/values never reach the model. Written by
	// Workspace Settings' Column Visibility tab (screens/13) and visible on ingest review (screens/07).
	ExcludedFromModel *bool `json:"excluded_from_model"`
}

// handleUpdateColumn serves PATCH /v1/workspaces/:id/columns/:columnId — FEAT-006's type-review
// override, FEAT-007's PII-flag override, and FEAT-010's model-exclusion override, all landing on
// the same column resource since screens/07 reviews the first two as tabs/steps over the same
// underlying rows and screens/13 manages the third.
func handleUpdateColumn(workspaces WorkspaceStore, members MemberLookupStore, columns ConfirmationColumnStore, auditWriter ConfirmationAuditWriter, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		var req updateColumnRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.InferredType == nil && req.PIIFlag == nil && req.ExcludedFromModel == nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Provide inferred_type, pii_flag, and/or excluded_from_model to update.",
				"code":  "validation_error",
			})
			return
		}

		columnID := chi.URLParam(r, "columnId")
		col, err := columns.GetByIDForWorkspace(r.Context(), columnID, workspaceID)
		if err != nil {
			if errors.Is(err, postgres.ErrColumnNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Column not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("getting column for override", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		if req.InferredType != nil {
			t, ok := validInferredTypes[*req.InferredType]
			if !ok {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("%q is not a recognized column type.", *req.InferredType),
					"code":  "validation_error",
				})
				return
			}
			if err := columns.UpdateInferredType(r.Context(), columnID, t); err != nil {
				logger.Error("updating column inferred type", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			col.InferredType = t
			// The response badge must describe the newly selected type, so move TypeConfidence to
			// that type's measured fit rate — exactly what UpdateInferredType's SQL does via COALESCE
			// (columns.go). A flat 1.0 here previously fabricated "100% of values fit this type" on a
			// column whose data mostly did not, precisely the silent-wrongness failure the docs name
			// as the product's differentiator. A column profiled before migration 00006 has no fit
			// rates; like the SQL's COALESCE, it keeps its existing TypeConfidence rather than
			// dropping to a confident-looking zero.
			if rate, ok := col.TypeFitRates[t]; ok {
				col.TypeConfidence = rate
			}
		}

		if req.PIIFlag != nil {
			pattern := ""
			if *req.PIIFlag {
				pattern = "manual"
				if req.PIIPattern != nil && *req.PIIPattern != "" {
					pattern = *req.PIIPattern
				} else if col.PIIFlag && col.PIIPattern != "" {
					// Re-confirming an already-detected flag (e.g. toggled off then back on in the
					// same review session) keeps its original detected pattern rather than being
					// relabeled "manual".
					pattern = col.PIIPattern
				}
			}
			// One timestamp for both writes below, so the column's pii_reviewed_at and its
			// pii_override audit entry describe the same instant rather than two instants a
			// round trip apart.
			reviewedAt := time.Now().UTC()
			if err := columns.UpdatePII(r.Context(), columnID, *req.PIIFlag, pattern, reviewedAt); err != nil {
				logger.Error("updating column pii flag", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			col.PIIFlag = *req.PIIFlag
			col.PIIPattern = pattern
			col.PIIReviewedAt = reviewedAt.Format(time.RFC3339)

			// FEAT-007: the override is logged as an explicit user decision here, in the same code
			// path as the write it describes (IMPLEMENTATION.md rule 7) — never reconciled later.
			//
			// A failed audit write fails the request. The alternative — persisting the override and
			// logging a warning — would leave a column whose model exposure changed with no record
			// of who changed it, which is the exact state schema.md's pii_override action type was
			// added to prevent. The column update is idempotent, so a retry is safe.
			if err := auditWriter.WritePIIOverride(r.Context(), workspaceID, claims.UserID); err != nil {
				logger.Error("writing pii override audit entry", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
		}

		if req.ExcludedFromModel != nil {
			if err := columns.UpdateExclusion(r.Context(), columnID, *req.ExcludedFromModel); err != nil {
				logger.Error("updating column model exclusion", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			col.ExcludedFromModel = *req.ExcludedFromModel
		}

		respondJSON(w, http.StatusOK, map[string]any{"column": toPublicColumn(col)})
	}
}

// validCardinalities mirrors schema.md's Relationship.cardinality enum, for validating an optional
// manual cardinality override on accept.
var validCardinalities = map[string]domain.Cardinality{
	"one_to_one":   domain.OneToOne,
	"one_to_many":  domain.OneToMany,
	"many_to_one":  domain.ManyToOne,
	"many_to_many": domain.ManyToMany,
}

// updateCandidateJoinRequest is the PATCH body for FEAT-009's accept/reject/manually-adjust decision.
type updateCandidateJoinRequest struct {
	Status string `json:"status"`
	// Cardinality and GrainNotes are optional manual-adjust fields, only meaningful when Status is
	// "accepted" — if Cardinality is omitted, it is computed from the columns' uniqueness (mirroring
	// relationship inference's own cardinality signal) rather than left to an arbitrary default.
	Cardinality *string `json:"cardinality"`
	GrainNotes  *string `json:"grain_notes"`
}

// handleUpdateCandidateJoin serves PATCH /v1/workspaces/:id/candidate-joins/:joinId — FEAT-009's
// accept/reject/manually-adjust decision on a single candidate join.
//
// Rejecting only updates the candidate join's own status; FEAT-009's "never silently reintroduced"
// guarantee is enforced downstream, by internal/ingest.Pipeline consulting
// CandidateJoinStore.ExistsRejectedForNamePair on any later ingest — not by anything this handler
// does.
//
// Accepting additionally writes a Relationship row into the semantic layer (schema.md: "Created when
// a CandidateJoin is accepted"). FEAT-019 (the full semantic layer feature) is Phase 2 and not
// authorized this wave; this handler performs only the single write FEAT-009 itself requires.
func handleUpdateCandidateJoin(
	workspaces WorkspaceStore,
	members MemberLookupStore,
	columns ConfirmationColumnStore,
	datasets ConfirmationDatasetStore,
	joins ConfirmationCandidateJoinStore,
	relationshipStore ConfirmationRelationshipStore,
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

		var req updateCandidateJoinRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		var status domain.CandidateJoinStatus
		switch req.Status {
		case string(domain.JoinAccepted):
			status = domain.JoinAccepted
		case string(domain.JoinRejected):
			status = domain.JoinRejected
		default:
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": `status must be "accepted" or "rejected".`,
				"code":  "validation_error",
			})
			return
		}

		var cardinality domain.Cardinality
		if req.Cardinality != nil {
			c, ok := validCardinalities[*req.Cardinality]
			if !ok {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("%q is not a recognized cardinality.", *req.Cardinality),
					"code":  "validation_error",
				})
				return
			}
			cardinality = c
		}

		joinID := chi.URLParam(r, "joinId")
		cj, err := joins.GetByID(r.Context(), joinID)
		if err != nil {
			if errors.Is(err, postgres.ErrCandidateJoinNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Candidate join not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("getting candidate join for decision", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		// Same non-enumeration response as a genuinely missing join — see requireWorkspaceOwnership's
		// doc comment for why this pattern is used throughout the API.
		if cj.WorkspaceID != workspaceID {
			respondJSON(w, http.StatusNotFound, map[string]string{
				"error": "Candidate join not found.",
				"code":  "not_found",
			})
			return
		}

		var createdRelationship *domain.Relationship
		if status == domain.JoinAccepted {
			rel, err := buildRelationship(r.Context(), columns, datasets, cj, cardinality, req.GrainNotes)
			if err != nil {
				logger.Error("computing relationship for accepted join", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			if err := relationshipStore.Create(r.Context(), rel); err != nil {
				logger.Error("storing accepted relationship", slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong. Try again.",
					"code":  "internal_error",
				})
				return
			}
			createdRelationship = &rel
		}

		if err := joins.UpdateStatus(r.Context(), joinID, status); err != nil {
			logger.Error("updating candidate join status", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		cj.Status = status

		resp := map[string]any{"candidate_join": toPublicCandidateJoin(cj)}
		if createdRelationship != nil {
			resp["relationship"] = toPublicRelationship(*createdRelationship)
		}
		respondJSON(w, http.StatusOK, resp)
	}
}

// buildRelationship assembles the Relationship row for an accepted candidate join. cardinality is
// used as-is when the caller supplied a manual override (FEAT-009's "manually adjust"); otherwise it
// is defaulted from the source/target columns' uniqueness, the same key-like signal relationship
// inference itself already computes (relationships.Uniqueness) — reused here rather than guessed
// fresh, so a candidate accepted without a manual override still gets a considered default rather
// than an arbitrary one.
func buildRelationship(ctx context.Context, columns ConfirmationColumnStore, datasets ConfirmationDatasetStore, cj domain.CandidateJoin, cardinality domain.Cardinality, grainNotes *string) (domain.Relationship, error) {
	notes := ""
	if grainNotes != nil {
		notes = *grainNotes
	}

	if cardinality == "" {
		computed, err := defaultCardinality(ctx, columns, datasets, cj)
		if err != nil {
			return domain.Relationship{}, err
		}
		cardinality = computed
	}

	return domain.Relationship{
		ID:             uuid.NewString(),
		WorkspaceID:    cj.WorkspaceID,
		SourceColumnID: cj.SourceColumnID,
		TargetColumnID: cj.TargetColumnID,
		Cardinality:    cardinality,
		GrainNotes:     notes,
		ConfirmedAt:    time.Now(),
	}, nil
}

// defaultCardinality scores each side of the join's uniqueness (distinct_count against its owning
// dataset's row_count) and reports the shape that best matches — one_to_one, one_to_many,
// many_to_one, or many_to_many — using relationships.UniqueCardinalityThreshold as the "key-like"
// cutoff. Both columns are looked up scoped to cj.WorkspaceID (via GetByIDForWorkspace) since the
// candidate join's own workspace has already been verified by its caller — never a bare,
// workspace-unscoped column fetch.
func defaultCardinality(ctx context.Context, columns ConfirmationColumnStore, datasets ConfirmationDatasetStore, cj domain.CandidateJoin) (domain.Cardinality, error) {
	sourceUnique, err := columnIsUnique(ctx, columns, datasets, cj.WorkspaceID, cj.SourceColumnID)
	if err != nil {
		return "", fmt.Errorf("scoring source column uniqueness: %w", err)
	}
	targetUnique, err := columnIsUnique(ctx, columns, datasets, cj.WorkspaceID, cj.TargetColumnID)
	if err != nil {
		return "", fmt.Errorf("scoring target column uniqueness: %w", err)
	}

	switch {
	case sourceUnique && targetUnique:
		return domain.OneToOne, nil
	case sourceUnique && !targetUnique:
		return domain.OneToMany, nil
	case !sourceUnique && targetUnique:
		return domain.ManyToOne, nil
	default:
		return domain.ManyToMany, nil
	}
}

// columnIsUnique reports whether columnID's distinct_count is at least
// relationships.UniqueCardinalityThreshold of its owning dataset's row_count — a primary/foreign-key
// shape, the same signal relationship inference itself computes via relationships.Uniqueness.
func columnIsUnique(ctx context.Context, columns ConfirmationColumnStore, datasets ConfirmationDatasetStore, workspaceID, columnID string) (bool, error) {
	col, err := columns.GetByIDForWorkspace(ctx, columnID, workspaceID)
	if err != nil {
		return false, fmt.Errorf("getting column %q: %w", columnID, err)
	}
	ds, err := datasets.GetByID(ctx, col.DatasetID)
	if err != nil {
		return false, fmt.Errorf("getting dataset %q for column %q: %w", col.DatasetID, columnID, err)
	}
	return relationships.Uniqueness(col.DistinctCount, ds.RowCount) >= uniqueCardinalityThreshold, nil
}
