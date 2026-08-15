package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	"github.com/ahmadkhidir/groundwork/server/internal/run"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
	"github.com/ahmadkhidir/groundwork/server/internal/suggestions"
)

// ObjectGetter downloads an object's bytes, satisfied by internal/storage.Client. The smart
// generation handler needs it to attach the engine views it profiles (run.PrepareViews downloads
// each dataset's normalized Parquet to a temp file first).
type ObjectGetter interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
}

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

// SuggestionBucket persists a workspace's model-generated question suggestions — FEAT-051's
// "add to the list" behavior. The deterministic strip is computed at read time and never stored;
// the model-generated set accumulates here so a user who picks one of ten never pays to regenerate
// the other nine, and can delete rows they no longer want. Satisfied by
// postgres.SuggestionQuestionRepository.
type SuggestionBucket interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SuggestionQuestion, error)
	Add(ctx context.Context, workspaceID string, questions []string) ([]domain.SuggestionQuestion, error)
	Delete(ctx context.Context, workspaceID, id string) error
}

// publicSuggestionQuestion is the wire shape of one persisted smart suggestion. The workspace id
// is deliberately omitted: the client already knows it, and the id doubles as the delete key.
type publicSuggestionQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

func toPublicSuggestions(rows []domain.SuggestionQuestion) []publicSuggestionQuestion {
	out := make([]publicSuggestionQuestion, 0, len(rows))
	for _, s := range rows {
		out = append(out, publicSuggestionQuestion{ID: s.ID, Question: s.Question})
	}
	return out
}

// suggestionError writes an error response carrying the request's correlation id in the body. The
// web tier reads the x-request-id header when CORS exposes it, but the body copy is the reliable
// transport through every proxy — a user who hits a 5xx can report the id and support can match it
// against the request log (api.go's requestLogger).
func suggestionError(w http.ResponseWriter, r *http.Request, status int, message, code string) {
	body := map[string]string{"error": message, "code": code}
	if reqID := middleware.GetReqID(r.Context()); reqID != "" {
		body["request_id"] = reqID
	}
	respondJSON(w, status, body)
}

// RegisterSuggestionRoutes wires FEAT-050's deterministic read, FEAT-051's smart-generation
// endpoint, and the persisted smart bucket (list + delete) onto r. The smart endpoint is registered
// only when every collaborator it needs is wired (users, engine, model, objects) — otherwise only
// the deterministic strip and bucket management exist.
func RegisterSuggestionRoutes(r chi.Router, workspaces WorkspaceStore, members MemberLookupStore, users UserTierLookup, source SuggestionSource, bucket SuggestionBucket, engineProvider SuggestionEngineProvider, modelClient SmartSuggestionModel, objects ObjectGetter, logger *slog.Logger) {
	if bucket == nil {
		return
	}
	r.Get("/workspaces/{workspaceId}/suggestions", handleListSuggestions(workspaces, members, source, bucket, logger))
	r.Delete("/workspaces/{workspaceId}/suggestions/{suggestionId}", handleDeleteSuggestion(workspaces, members, users, bucket, logger))
	if users != nil && engineProvider != nil && modelClient != nil && objects != nil {
		r.Post("/workspaces/{workspaceId}/suggestions/smart", handleSmartSuggestions(workspaces, members, users, source, bucket, engineProvider, modelClient, objects, logger))
	}
}

// handleListSuggestions serves GET /v1/workspaces/:id/suggestions — the deterministic, metadata-only
// suggestions strip (FEAT-050) plus the workspace's persisted smart-suggestion bucket (FEAT-051).
// It is a pure Postgres read + template expansion: no engine query, no model call, no egress entry.
// Access is the ordinary read boundary (owner OR read-only member); the web UI hides the strip for
// read-only members, but suggestions are harmless metadata reads like any other workspace read.
func handleListSuggestions(workspaces WorkspaceStore, members MemberLookupStore, source SuggestionSource, bucket SuggestionBucket, logger *slog.Logger) http.HandlerFunc {
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
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}

		bucketRows, err := bucket.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing smart suggestion bucket", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"suggestions": suggested,
			"smart":       toPublicSuggestions(bucketRows),
		})
	}
}

// handleSmartSuggestions serves POST /v1/workspaces/:id/suggestions/smart — FEAT-051's on-demand,
// Pro/Team-gated smart generation: wake the engine, profile safe columns, call the model once
// through the shared payload seam, and APPEND the 5-10 data-informed questions to the workspace's
// persisted bucket (never replace): the user who picks one question is not charged to regenerate
// the other nine, and can delete rows from the bucket. The deterministic strip stays visible
// alongside.
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
	bucket SuggestionBucket,
	engineProvider SuggestionEngineProvider,
	modelClient SmartSuggestionModel,
	objects ObjectGetter,
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
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
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
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}
		columns, err := source.Columns(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing columns for smart suggestions", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}
		metrics, err := source.Metrics(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing metric definitions for smart suggestions", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}

		candidates, err := suggestions.Generate(r.Context(), workspaceID, source)
		if err != nil {
			logger.Error("generating candidate suggestions", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}

		// The persisted bucket is read up front: generation deduplicates against it AND against the
		// deterministic set, so a question already on the list is never generated twice.
		bucketRows, err := bucket.ListByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing smart suggestion bucket for generation", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}

		// Wake → attach the dataset views → profile (FEAT-051's honest latency: the engine must
		// actually run queries). Engine views are session-scoped and reference temp files, so they
		// are created here (idempotently) and their temp files cleaned up after profiling.
		handle, err := engineProvider.Acquire(r.Context(), workspaceID)
		if err != nil {
			if err == engine.ErrEngineUnavailable {
				suggestionError(w, r, http.StatusServiceUnavailable, "The engine is taking longer than usual to wake. Try again.", "engine_unavailable")
				return
			}
			logger.Error("acquiring engine for smart suggestions", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}
		cleanups, viewErr := run.PrepareViews(r.Context(), handle, workspaceID, datasets, objects)
		if viewErr != nil {
			releaseErr := engineProvider.Release(handle)
			if releaseErr != nil {
				logger.Error("releasing engine after view attach failure", slog.String("error", releaseErr.Error()))
			}
			logger.Error("attaching engine views for smart suggestions", slog.String("error", viewErr.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Couldn't read your data to generate questions. Try again.", "data_unavailable")
			return
		}
		profiles, profileErr := suggestions.Profile(r.Context(), handle, datasets, columns)
		for _, done := range cleanups {
			done()
		}
		releaseErr := engineProvider.Release(handle)
		if profileErr != nil {
			logger.Error("profiling for smart suggestions", slog.String("error", profileErr.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Couldn't read your data to generate questions. Try again.", "data_unavailable")
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
			suggestionError(w, r, http.StatusBadGateway, "Could not generate smart questions right now. Try again.", "model_error")
			return
		}

		smart, err := suggestions.ParseModelResponse(text, safeColumnNames(columns), metricNames(metrics))
		if err != nil {
			logger.Error("parsing smart suggestion response", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusBadGateway, "Could not generate smart questions right now. Try again.", "model_error")
			return
		}

		// FEAT-051's "add to the list" behavior: append (never replace) the model's questions to
		// the persisted bucket, skipping any that duplicate the deterministic set or an existing
		// row. The user's accumulated list is the source of truth — a question already on it is
		// never generated twice.
		existing := make(map[string]bool, len(candidates)+len(bucketRows)+len(smart))
		for _, s := range candidates {
			existing[s] = true
		}
		for _, s := range bucketRows {
			existing[s.Question] = true
		}
		fresh := make([]string, 0, len(smart))
		for _, s := range smart {
			if !existing[s] {
				fresh = append(fresh, s)
				existing[s] = true
			}
		}
		bucketRows, err = bucket.Add(r.Context(), workspaceID, fresh)
		if err != nil {
			logger.Error("persisting smart suggestions", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"suggestions":     candidates,
			"smart":           toPublicSuggestions(bucketRows),
			"model_generated": true,
		})
	}
}

// handleDeleteSuggestion serves DELETE /v1/workspaces/:id/suggestions/:suggestionId — the user's
// cleanup affordance for the persisted smart-suggestion bucket (FEAT-051's "add to the list"
// behavior). Deleting a suggestion is a workspace mutation (owner or writable member; the
// tier-downgrade gate applies), and the row must belong to the workspace — a cross-workspace id
// reads as not-found, never a probe.
func handleDeleteSuggestion(workspaces WorkspaceStore, members MemberLookupStore, users UserTierLookup, bucket SuggestionBucket, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}
		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceMutationAccess(w, r, workspaces, members, users, claims.UserID, workspaceID); !ok {
			return
		}

		err := bucket.Delete(r.Context(), workspaceID, chi.URLParam(r, "suggestionId"))
		if err != nil {
			if errors.Is(err, postgres.ErrSuggestionQuestionNotFound) {
				respondJSON(w, http.StatusNotFound, map[string]string{"error": "Suggestion not found.", "code": "not_found"})
				return
			}
			logger.Error("deleting smart suggestion", slog.String("error", err.Error()))
			suggestionError(w, r, http.StatusInternalServerError, "Something went wrong. Try again.", "internal_error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
