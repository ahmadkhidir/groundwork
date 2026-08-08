package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	"github.com/ahmadkhidir/groundwork/server/internal/run"
	"github.com/ahmadkhidir/groundwork/server/internal/suggestions"
)

// SuggestionSource is the persistence seam GET /workspaces/:id/suggestions depends on — the same
// four listers suggestions.Generate reads, satisfied by the postgres repositories. Defined here
// (consumer-side, matching the codebase's narrow-seam pattern) so the handler's tests use a small
// fake.
type SuggestionSource interface {
	Datasets(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
	Columns(ctx context.Context, workspaceID string) ([]domain.Column, error)
	Relationships(ctx context.Context, workspaceID string) ([]domain.Relationship, error)
	Metrics(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error)
}

// SmartSuggestionModel is the model seam POST /workspaces/:id/suggestions/smart depends on,
// satisfied by *model.Client. Complete is the same egress-logged, metered path every model call
// uses — the smart-generation call must flow through it (FEAT-051's egress/metering ACs).
type SmartSuggestionModel interface {
	Complete(ctx context.Context, workspaceID, userID, system string, payload model.Payload) (string, error)
}

// SuggestionEngineProvider is the engine seam the smart-generation handler needs — the full
// Provider (Acquire/Release), because profiling queries require a leased engine (FEAT-051's
// wake → profile → generate sequence). Satisfied by engine.Provider.
type SuggestionEngineProvider interface {
	Acquire(ctx context.Context, workspaceID string) (engine.Handle, error)
	Release(handle engine.Handle) error
}

// RegisterSuggestionRoutes wires FEAT-050's deterministic read and FEAT-051's smart-generation
// endpoint onto r. The smart endpoint is registered only when every collaborator it needs is
// wired (users, engine, model) — otherwise only the deterministic strip exists.
func RegisterSuggestionRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, users UserTierLookup, source SuggestionSource, engineProvider SuggestionEngineProvider, modelClient SmartSuggestionModel, logger *slog.Logger) {
	r.Get("/workspaces/{workspaceId}/suggestions", handleListSuggestions(workspaces, members, source, logger))
	if users != nil && engineProvider != nil && modelClient != nil {
		r.Post("/workspaces/{workspaceId}/suggestions/smart", handleSmartSuggestions(workspaces, members, users, source, engineProvider, modelClient, logger))
	}
}

// handleListSuggestions serves GET /v1/workspaces/:id/suggestions — the deterministic, metadata-only
// suggestions strip (FEAT-050). It is a pure Postgres read + template expansion: no engine query, no
// model call, no egress entry. Access is the ordinary read boundary (owner OR read-only member); the
// web UI hides the strip for read-only members, but suggestions are harmless metadata reads like any
// other workspace read.
func handleListSuggestions(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, _, ok := requireWorkspaceAccess(w, r, workspaces, members, claims.UserID, workspaceID); !ok {
			return
		}

		suggested, err := suggestions.Generate(r.Context(), workspaceID, source)
		if err != nil {
			logger.Error("generating question suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"suggestions": suggested})
	}
}

// handleSmartSuggestions serves POST /v1/workspaces/:id/suggestions/smart — FEAT-051's on-demand,
// Pro/Team-gated smart generation: wake the engine, profile safe columns, call the model once
// through the shared payload seam, and return 5-10 data-informed questions.
//
// Egress and metering are inherited from model.Client.Complete (the same fail-closed
// egress-before-call and token-metering path every model call uses); the profiling queries are
// engine work (engine-seconds), never model calls. Read-only members cannot trigger generation
// (requireWorkspaceMutationAccess), and Free-tier accounts are rejected with 403 upgrade_required
// before any engine or model work happens.
func handleSmartSuggestions(
	workspaces WorkspaceStore,
	members MemberLookupStore,
	users UserTierLookup,
	source SuggestionSource,
	engineProvider SuggestionEngineProvider,
	modelClient SmartSuggestionModel,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, users, claims.UserID, workspaceID); !ok {
			return
		}

		// Tier gate (monetization.md): smart generation is Pro/Team. Authoritative tier from the
		// store, never the session claim.
		user, _, err := users.FindByID(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("looking up user for smart-generation tier gate",
				slog.String("workspace_id", workspaceID), slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		if user.Tier != domain.TierPro && user.Tier != domain.TierTeam {
			respondJSON(w, http.StatusForbidden, map[string]string{
				"error": "Smart question generation is available on the Pro and Team plans. Upgrade to generate data-informed questions.",
				"code":  "upgrade_required",
			})
			return
		}

		// The deterministic candidates + metadata the model may rephrase (never invent past it).
		datasets, err := source.Datasets(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing datasets for smart suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again.", "code": "internal_error"})
			return
		}
		columns, err := source.Columns(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing columns for smart suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again.", "code": "internal_error"})
			return
		}
		metrics, err := source.Metrics(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing metric definitions for smart suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again.", "code": "internal_error"})
			return
		}

		candidates, err := suggestions.Generate(r.Context(), workspaceID, source)
		if err != nil {
			logger.Error("generating candidate suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again.", "code": "internal_error"})
			return
		}

		// Wake → profile (FEAT-051's honest latency: the engine must actually run queries).
		handle, err := engineProvider.Acquire(r.Context(), workspaceID)
		if err != nil {
			if err == engine.ErrEngineUnavailable {
				respondJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "The engine is taking longer than usual to wake. Try again.",
					"code":  "engine_unavailable",
				})
				return
			}
			logger.Error("acquiring engine for smart suggestions", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again.", "code": "internal_error"})
			return
		}
		profiles, profileErr := suggestions.Profile(r.Context(), handle, datasets, columns)
		releaseErr := engineProvider.Release(handle)
		if profileErr != nil {
			logger.Error("profiling for smart suggestions", slog.String("error", profileErr.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again.", "code": "internal_error"})
			return
		}
		if releaseErr != nil {
			logger.Error("releasing engine after smart profiling", slog.String("error", releaseErr.Error()))
		}

		tables := buildSuggestionTables(datasets, columns)
		payload := model.BuildSuggestionsPayload(tables, candidates, profiles)

		text, err := modelClient.Complete(r.Context(), workspaceID, claims.UserID, suggestions.SuggestionsSystemPrompt, payload)
		if err != nil {
			logger.Error("smart suggestion model call", slog.String("error", err.Error()))
			respondJSON(w, http.StatusBadGateway, map[string]string{
				"error": "Could not generate smart questions right now. Try again.",
				"code":  "model_error",
			})
			return
		}

		smart, err := suggestions.ParseModelResponse(text, safeColumnNames(columns), metricNames(metrics))
		if err != nil {
			logger.Error("parsing smart suggestion response", slog.String("error", err.Error()))
			respondJSON(w, http.StatusBadGateway, map[string]string{
				"error": "Could not generate smart questions right now. Try again.",
				"code":  "model_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"suggestions": smart, "model_generated": true})
	}
}

// buildSuggestionTables assembles the model.TableMetadata for every complete dataset, using the
// same canonical view names the executor teaches the model.
func buildSuggestionTables(datasets []domain.Dataset, columns []domain.Column) []model.TableMetadata {
	colsByDataset := make(map[string][]domain.Column)
	for _, c := range columns {
		colsByDataset[c.DatasetID] = append(colsByDataset[c.DatasetID], c)
	}
	var out []model.TableMetadata
	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		t := model.TableMetadata{DatasetID: ds.ID, Name: run.TableNameForDataset(ds)}
		for _, c := range colsByDataset[ds.ID] {
			t.Columns = append(t.Columns, model.ColumnMetadata{
				Name:              c.Name,
				Type:              string(c.InferredType),
				PIIFlag:           c.PIIFlag,
				ExcludedFromModel: c.ExcludedFromModel,
				NullRate:          c.NullRate,
				DistinctCount:     c.DistinctCount,
			})
		}
		out = append(out, t)
	}
	return out
}

// safeColumnNames returns the names of every column that is neither PII-flagged nor excluded —
// the entity vocabulary ParseModelResponse checks the model's output against.
func safeColumnNames(columns []domain.Column) []string {
	var out []string
	for _, c := range columns {
		if !c.PIIFlag && !c.ExcludedFromModel {
			out = append(out, c.Name)
		}
	}
	return out
}

func metricNames(metrics []domain.MetricDefinition) []string {
	out := make([]string, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, m.Name)
	}
	return out
}
