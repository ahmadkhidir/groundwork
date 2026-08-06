// This file implements FEAT-019 (Semantic Layer Storage)'s HTTP surface —
// screens/15-semantic-layer.md's three tabs (Relationships, Metric Definitions, Corrections) and
// screens/04-workspace-home.md's summary panel.
//
// FEAT-009's accepted-join write (Create) already lands rows in the `relationships` table
// (confirmation.go). This file adds the read/edit surface over those same rows, plus the full
// metric-definition CRUD and correction read surface that only FEAT-019 needs. It does NOT build any
// model call, run, or step — FEAT-013 (Phase 2, next wave) is the first feature to consume the
// semantic layer as grounding context, via internal/semantic.AssembleGroundingContext.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// SemanticRelationshipStore is the persistence seam for reading and editing confirmed relationships
// (FEAT-019's Relationships tab), satisfied by internal/store/postgres.RelationshipRepository. A
// superset of ConfirmationRelationshipStore (confirmation.go), which only needs Create.
type SemanticRelationshipStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Relationship, error)
	GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Relationship, error)
	Update(ctx context.Context, id string, cardinality domain.Cardinality, grainNotes string) error
}

// SemanticMetricDefinitionStore is the persistence seam for the Metric Definitions tab's full CRUD,
// satisfied by internal/store/postgres.MetricDefinitionRepository.
type SemanticMetricDefinitionStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error)
	GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.MetricDefinition, error)
	Create(ctx context.Context, m domain.MetricDefinition) error
	Update(ctx context.Context, id, name, sqlExpression, description string, updatedAt time.Time) error
	DeleteForWorkspace(ctx context.Context, id, workspaceID string) error
}

// SemanticCorrectionStore is the persistence seam for the Corrections tab, satisfied by
// internal/store/postgres.CorrectionRepository. FEAT-019 shipped the read side; FEAT-020 (a later
// wave of the same phase) extends it with the write side: Create (api/runs.go captures a Correction
// row when a user's edited SQL starts a new run) and Delete (the Corrections tab's remove action,
// AC3 "user can view and remove a stored correction").
type SemanticCorrectionStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Correction, error)
	Create(ctx context.Context, c domain.Correction) error
	Delete(ctx context.Context, workspaceID, id string) error
}

// SemanticSummaryStore backs screens/04-workspace-home.md's compact summary panel, satisfied by
// internal/store/postgres.SemanticSummaryRepository.
type SemanticSummaryStore interface {
	CountRelationships(ctx context.Context, workspaceID string) (int, error)
	CountMetricDefinitions(ctx context.Context, workspaceID string) (int, error)
}

// RegisterSemanticRoutes wires FEAT-019's endpoints onto r.
func RegisterSemanticRoutes(
	r chi.Router,
	workspaces WorkspaceStore,
	relationships SemanticRelationshipStore,
	metrics SemanticMetricDefinitionStore,
	corrections SemanticCorrectionStore,
	summary SemanticSummaryStore,
	logger *slog.Logger,
) {
	r.Get("/workspaces/{workspaceId}/semantic-layer/summary", handleSemanticSummary(workspaces, summary, logger))

	r.Get("/workspaces/{workspaceId}/semantic-layer/relationships", handleListSemanticRelationships(workspaces, relationships, logger))
	r.Patch("/workspaces/{workspaceId}/semantic-layer/relationships/{relationshipId}", handleUpdateSemanticRelationship(workspaces, relationships, logger))

	r.Get("/workspaces/{workspaceId}/semantic-layer/metrics", handleListMetricDefinitions(workspaces, metrics, logger))
	r.Post("/workspaces/{workspaceId}/semantic-layer/metrics", handleCreateMetricDefinition(workspaces, metrics, logger))
	r.Patch("/workspaces/{workspaceId}/semantic-layer/metrics/{metricId}", handleUpdateMetricDefinition(workspaces, metrics, logger))
	r.Delete("/workspaces/{workspaceId}/semantic-layer/metrics/{metricId}", handleDeleteMetricDefinition(workspaces, metrics, logger))

	r.Get("/workspaces/{workspaceId}/semantic-layer/corrections", handleListCorrections(workspaces, corrections, logger))
	r.Delete("/workspaces/{workspaceId}/semantic-layer/corrections/{correctionId}", handleDeleteCorrection(workspaces, corrections, logger))
}

func internalErrorResponse(w http.ResponseWriter) {
	respondJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "Something went wrong. Try again.",
		"code":  "internal_error",
	})
}

/* -------------------------------------------------------------------------------------------- */
/* Summary                                                                                        */
/* -------------------------------------------------------------------------------------------- */

// handleSemanticSummary serves GET /v1/workspaces/:id/semantic-layer/summary —
// screens/04-workspace-home.md's compact "N relationships, M metric definitions" panel.
func handleSemanticSummary(workspaces WorkspaceStore, summary SemanticSummaryStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		relationshipCount, err := summary.CountRelationships(r.Context(), workspaceID)
		if err != nil {
			logger.Error("counting relationships for semantic layer summary", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}
		metricCount, err := summary.CountMetricDefinitions(r.Context(), workspaceID)
		if err != nil {
			logger.Error("counting metric definitions for semantic layer summary", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"relationship_count":      relationshipCount,
			"metric_definition_count": metricCount,
		})
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Relationships                                                                                  */
/* -------------------------------------------------------------------------------------------- */

// handleListSemanticRelationships serves GET /v1/workspaces/:id/semantic-layer/relationships —
// screens/15-semantic-layer.md's Relationships tab, and the FEAT-009-seeded rows this feature was
// deferred to make readable.
func handleListSemanticRelationships(workspaces WorkspaceStore, relationships SemanticRelationshipStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		list, err := relationships.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing semantic layer relationships", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		out := make([]publicRelationship, 0, len(list))
		for _, rel := range list {
			out = append(out, toPublicRelationship(rel))
		}
		respondJSON(w, http.StatusOK, map[string]any{"relationships": out})
	}
}

// updateSemanticRelationshipRequest is the PATCH body for FEAT-019's Relationships tab Edit action.
// Both fields are pointers, mirroring updateColumnRequest's "not supplied vs. explicit value"
// distinction: editing only the grain notes must not silently reset the cardinality, and vice versa.
type updateSemanticRelationshipRequest struct {
	Cardinality *string `json:"cardinality"`
	GrainNotes  *string `json:"grain_notes"`
}

// handleUpdateSemanticRelationship serves PATCH
// /v1/workspaces/:id/semantic-layer/relationships/:relationshipId — FEAT-019's Edit action on the
// Relationships tab. Per screens/15's edge case, this never retroactively alters an in-progress
// run's already-generated SQL; it only changes what a future run's grounding context sees, which is
// automatic here since this handler has no notion of an in-flight run at all.
func handleUpdateSemanticRelationship(workspaces WorkspaceStore, relationships SemanticRelationshipStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		var req updateSemanticRelationshipRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Cardinality == nil && req.GrainNotes == nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Provide cardinality and/or grain_notes to update.",
				"code":  "validation_error",
			})
			return
		}

		relationshipID := chi.URLParam(r, "relationshipId")
		rel, err := relationships.GetByIDForWorkspace(r.Context(), relationshipID, workspaceID)
		if err != nil {
			if errors.Is(err, postgres.ErrRelationshipNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Relationship not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("getting relationship for edit", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		cardinality := rel.Cardinality
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

		grainNotes := rel.GrainNotes
		if req.GrainNotes != nil {
			grainNotes = *req.GrainNotes
		}

		if err := relationships.Update(r.Context(), relationshipID, cardinality, grainNotes); err != nil {
			logger.Error("updating relationship", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}
		rel.Cardinality = cardinality
		rel.GrainNotes = grainNotes

		respondJSON(w, http.StatusOK, map[string]any{"relationship": toPublicRelationship(rel)})
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Metric definitions                                                                             */
/* -------------------------------------------------------------------------------------------- */

// publicMetricDefinition is a metric definition as returned by the semantic layer API.
type publicMetricDefinition struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Name          string `json:"name"`
	SQLExpression string `json:"sql_expression"`
	Description   string `json:"description"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func toPublicMetricDefinition(m domain.MetricDefinition) publicMetricDefinition {
	return publicMetricDefinition{
		ID:            m.ID,
		WorkspaceID:   m.WorkspaceID,
		Name:          m.Name,
		SQLExpression: m.SQLExpression,
		Description:   m.Description,
		CreatedAt:     m.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     m.UpdatedAt.Format(time.RFC3339),
	}
}

// handleListMetricDefinitions serves GET /v1/workspaces/:id/semantic-layer/metrics —
// screens/15-semantic-layer.md's Metric Definitions tab.
func handleListMetricDefinitions(workspaces WorkspaceStore, metrics SemanticMetricDefinitionStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		list, err := metrics.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing metric definitions", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		out := make([]publicMetricDefinition, 0, len(list))
		for _, m := range list {
			out = append(out, toPublicMetricDefinition(m))
		}
		respondJSON(w, http.StatusOK, map[string]any{"metric_definitions": out})
	}
}

// createMetricDefinitionRequest is the POST body for creating a new metric definition.
//
// screens/15-semantic-layer.md documents editing an EXISTING metric definition explicitly
// ("PATCH-updates-in-place"); it does not show an "add new metric" affordance in as many words.
// Creation is implemented anyway per this wave's explicit scope grant ("Metric definitions: full
// CRUD... is in scope for FEAT-019") and because a metric definitions store with no way to add a
// first metric could never leave its Empty state — an assumption, not an invented requirement,
// flagged here for product-shaper if the screen should instead show creation as out of scope.
type createMetricDefinitionRequest struct {
	Name          string `json:"name"`
	SQLExpression string `json:"sql_expression"`
	Description   string `json:"description"`
}

// handleCreateMetricDefinition serves POST /v1/workspaces/:id/semantic-layer/metrics.
func handleCreateMetricDefinition(workspaces WorkspaceStore, metrics SemanticMetricDefinitionStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		var req createMetricDefinitionRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		name := strings.TrimSpace(req.Name)
		sqlExpression := strings.TrimSpace(req.SQLExpression)
		if name == "" || sqlExpression == "" {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "A metric definition needs both a name and a SQL expression.",
				"code":  "validation_error",
			})
			return
		}

		now := time.Now()
		m := domain.MetricDefinition{
			ID:            uuid.NewString(),
			WorkspaceID:   workspaceID,
			Name:          name,
			SQLExpression: sqlExpression,
			Description:   strings.TrimSpace(req.Description),
			CreatedAt:     now,
			UpdatedAt:     now,
		}

		if err := metrics.Create(r.Context(), m); err != nil {
			if errors.Is(err, postgres.ErrMetricDefinitionNameTaken) {
				respondJSON(w, http.StatusConflict, map[string]string{
					"error": fmt.Sprintf("A metric named %q already exists in this workspace.", name),
					"code":  "name_taken",
				})
				return
			}
			logger.Error("creating metric definition", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		respondJSON(w, http.StatusCreated, map[string]any{"metric_definition": toPublicMetricDefinition(m)})
	}
}

// updateMetricDefinitionRequest is the PATCH body for FEAT-019's Metric Definitions tab Edit action.
// Every field is a pointer so an edit touching only, say, the description doesn't have to resend the
// SQL expression verbatim.
type updateMetricDefinitionRequest struct {
	Name          *string `json:"name"`
	SQLExpression *string `json:"sql_expression"`
	Description   *string `json:"description"`
}

// handleUpdateMetricDefinition serves PATCH /v1/workspaces/:id/semantic-layer/metrics/:metricId.
//
// Per screens/15's edge case, editing a metric definition that's actively referenced by an
// in-progress run is saved immediately but does not retroactively alter that run's already-generated
// SQL — it applies from the next run onward. As with relationship edits above, this falls out
// naturally: this handler has no notion of an in-flight run.
func handleUpdateMetricDefinition(workspaces WorkspaceStore, metrics SemanticMetricDefinitionStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		var req updateMetricDefinitionRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Name == nil && req.SQLExpression == nil && req.Description == nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Provide name, sql_expression, and/or description to update.",
				"code":  "validation_error",
			})
			return
		}

		metricID := chi.URLParam(r, "metricId")
		m, err := metrics.GetByIDForWorkspace(r.Context(), metricID, workspaceID)
		if err != nil {
			if errors.Is(err, postgres.ErrMetricDefinitionNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Metric definition not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("getting metric definition for edit", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		name := m.Name
		if req.Name != nil {
			name = strings.TrimSpace(*req.Name)
			if name == "" {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "A metric definition's name cannot be blank.",
					"code":  "validation_error",
				})
				return
			}
		}
		sqlExpression := m.SQLExpression
		if req.SQLExpression != nil {
			sqlExpression = strings.TrimSpace(*req.SQLExpression)
			if sqlExpression == "" {
				respondJSON(w, http.StatusBadRequest, map[string]string{
					"error": "A metric definition's SQL expression cannot be blank.",
					"code":  "validation_error",
				})
				return
			}
		}
		description := m.Description
		if req.Description != nil {
			description = strings.TrimSpace(*req.Description)
		}

		now := time.Now()
		if err := metrics.Update(r.Context(), metricID, name, sqlExpression, description, now); err != nil {
			if errors.Is(err, postgres.ErrMetricDefinitionNameTaken) {
				respondJSON(w, http.StatusConflict, map[string]string{
					"error": fmt.Sprintf("A metric named %q already exists in this workspace.", name),
					"code":  "name_taken",
				})
				return
			}
			logger.Error("updating metric definition", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		m.Name = name
		m.SQLExpression = sqlExpression
		m.Description = description
		m.UpdatedAt = now

		respondJSON(w, http.StatusOK, map[string]any{"metric_definition": toPublicMetricDefinition(m)})
	}
}

// handleDeleteMetricDefinition serves DELETE /v1/workspaces/:id/semantic-layer/metrics/:metricId.
func handleDeleteMetricDefinition(workspaces WorkspaceStore, metrics SemanticMetricDefinitionStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		metricID := chi.URLParam(r, "metricId")
		// Verified for existence/ownership first (rather than a blind DELETE that always returns
		// 204) so a caller gets an honest 404 for a metric that never existed or belongs to another
		// workspace, matching this API's non-enumeration pattern elsewhere.
		if _, err := metrics.GetByIDForWorkspace(r.Context(), metricID, workspaceID); err != nil {
			if errors.Is(err, postgres.ErrMetricDefinitionNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Metric definition not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("getting metric definition for delete", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		if err := metrics.DeleteForWorkspace(r.Context(), metricID, workspaceID); err != nil {
			logger.Error("deleting metric definition", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		respondJSON(w, http.StatusNoContent, nil)
	}
}

/* -------------------------------------------------------------------------------------------- */
/* Corrections                                                                                    */
/* -------------------------------------------------------------------------------------------- */

// publicCorrection is a correction as returned by the semantic layer API.
type publicCorrection struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	Description    string `json:"description"`
	OriginalLogic  string `json:"original_logic"`
	CorrectedLogic string `json:"corrected_logic"`
	// OriginatingRunID is empty when the originating run has since been deleted (migration 00005's
	// ON DELETE SET NULL) — screens/15's documented edge case. The web client disables the "view
	// originating run" link exactly when this is absent, rather than this handler pre-deciding a
	// link URL that may point at a now-nonexistent run.
	OriginatingRunID string `json:"originating_run_id,omitempty"`
	CreatedAt        string `json:"created_at"`
}

func toPublicCorrection(c domain.Correction) publicCorrection {
	return publicCorrection{
		ID:               c.ID,
		WorkspaceID:      c.WorkspaceID,
		Description:      c.Description,
		OriginalLogic:    c.OriginalLogic,
		CorrectedLogic:   c.CorrectedLogic,
		OriginatingRunID: c.OriginatingRunID,
		CreatedAt:        c.CreatedAt.Format(time.RFC3339),
	}
}

// handleListCorrections serves GET /v1/workspaces/:id/semantic-layer/corrections —
// screens/15-semantic-layer.md's Corrections tab. Legitimately returns an empty list until FEAT-020
// (a later wave) starts writing rows — see corrections.go's doc comment.
func handleListCorrections(workspaces WorkspaceStore, corrections SemanticCorrectionStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		list, err := corrections.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing corrections", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		out := make([]publicCorrection, 0, len(list))
		for _, c := range list {
			out = append(out, toPublicCorrection(c))
		}
		respondJSON(w, http.StatusOK, map[string]any{"corrections": out})
	}
}

// handleDeleteCorrection serves DELETE
// /v1/workspaces/:id/semantic-layer/corrections/:correctionId — FEAT-020 AC3's "user can view and
// remove a stored correction", screens/15-semantic-layer.md's Corrections tab remove affordance.
//
// Ownership is enforced by the store's workspace-scoped DELETE (corrections.go): a correction whose
// id exists but belongs to a different workspace comes back as ErrCorrectionNotFound and reads as a
// plain 404, matching this API's non-enumeration pattern (a cross-workspace correction is never
// confirmed to exist). Deleting from a workspace the caller does not own is already blocked by
// requireWorkspaceOwnership above.
func handleDeleteCorrection(workspaces WorkspaceStore, corrections SemanticCorrectionStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		correctionID := chi.URLParam(r, "correctionId")
		if err := corrections.Delete(r.Context(), workspaceID, correctionID); err != nil {
			if errors.Is(err, postgres.ErrCorrectionNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{
					"error": "Correction not found.",
					"code":  "not_found",
				})
				return
			}
			logger.Error("deleting correction", slog.String("error", err.Error()))
			internalErrorResponse(w)
			return
		}

		respondJSON(w, http.StatusNoContent, nil)
	}
}
