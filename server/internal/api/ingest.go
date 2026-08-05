package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/ingest"
)

// IngestDatasetStore is the persistence seam RegisterIngestRoutes depends on for reading dataset
// state, satisfied by internal/store/postgres.DatasetRepository.
type IngestDatasetStore interface {
	ListInProgressByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
}

// IngestPipeline is the seam RegisterIngestRoutes depends on for starting ingest, satisfied by
// internal/ingest.Pipeline.
type IngestPipeline interface {
	Start(datasetID string)
}

// ingestStreamPollInterval is how often the SSE handler re-reads dataset state from Postgres and the
// in-memory Tracker. Not a config.Config threshold (rule 12 scopes that to tunable product behavior,
// e.g. size ceilings and idle timeouts) — this is a fixed internal implementation detail of how the
// stream refreshes, not something a deployment would ever need to change.
const ingestStreamPollInterval = 500 * time.Millisecond

// stepOrder defines "least advanced" for computing one overall step across a batch of datasets —
// screens/06-ingest-progress.md shows a single step indicator for the whole upload batch, not one
// per file.
var stepOrder = map[ingest.Step]int{
	ingest.StepProvisioning:  0,
	ingest.StepLoading:       1,
	ingest.StepProfiling:     2,
	ingest.StepRelationships: 3,
	ingest.StepFailed:        99,
}

// RegisterIngestRoutes wires FEAT-004's endpoints onto r.
func RegisterIngestRoutes(r chi.Router, workspaces WorkspaceStore, datasets IngestDatasetStore, pipeline IngestPipeline, tracker *ingest.Tracker, logger *slog.Logger) {
	r.Post("/workspaces/{workspaceId}/ingest", handleStartIngest(workspaces, datasets, pipeline, logger))
	r.Get("/workspaces/{workspaceId}/ingest/stream", handleIngestStream(workspaces, datasets, tracker, logger))
}

// publicIngestDataset is one dataset's live ingest state, as sent to the client.
type publicIngestDataset struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Step     string `json:"step"`
	Error    string `json:"error,omitempty"`
}

// ingestStreamEvent is the SSE payload screens/06-ingest-progress.md's step indicator renders from.
type ingestStreamEvent struct {
	Datasets []publicIngestDataset `json:"datasets"`
	// OverallStep is the least-advanced step across every dataset in the batch — what the single,
	// shared step indicator shows — or "failed" if any dataset failed.
	OverallStep string `json:"overall_step"`
	// Done is true once every dataset has left this package's scope (handed off to "profiling" or
	// beyond) or failed — the client's signal to stop polling and, on success, auto-navigate to
	// Ingest Confirmation (screens/06's Data Flow step 3). It intentionally does NOT wait for
	// profiling/relationship-inference to finish, since FEAT-004 hands off before those run.
	Done bool `json:"done"`
}

// handleStartIngest serves FEAT-004's POST /api/workspaces/:id/ingest.
//
// Starts (or, idempotently, no-ops on) ingest for every dataset in the workspace that is still
// "pending" — the batch File Upload just created. Returns immediately; ingest itself runs in the
// background, decoupled from this request (see ingest.Pipeline.Start's doc comment on why that
// matters for the "tab closed mid-ingest" edge case).
func handleStartIngest(workspaces WorkspaceStore, datasets IngestDatasetStore, pipeline IngestPipeline, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		inProgress, err := datasets.ListInProgressByWorkspace(r.Context(), workspaceID)
		if err != nil {
			logger.Error("listing datasets to start ingest", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		started := make([]string, 0, len(inProgress))
		for _, ds := range inProgress {
			// Pipeline.Start is itself idempotent (a no-op unless the dataset is "pending"), so
			// calling it for every in-progress dataset — including ones already provisioning or
			// profiling — is safe and lets this endpoint double as "resume watching" for a client
			// that reconnects mid-ingest.
			pipeline.Start(ds.ID)
			started = append(started, ds.ID)
		}

		respondJSON(w, http.StatusAccepted, map[string]any{"dataset_ids": started})
	}
}

// handleIngestStream serves FEAT-004's GET /api/workspaces/:id/ingest/stream — screens/06's live
// status stream.
//
// Reads live state (the in-memory Tracker layered over the persisted ingest_status) rather than
// having the Ingest Pipeline push directly into this connection, specifically so a client that
// disconnects and reconnects (screens/06's tab-close edge case) sees accurate current state on
// reconnect instead of a stream that only makes sense to whichever request started it.
func handleIngestStream(workspaces WorkspaceStore, datasets IngestDatasetStore, tracker *ingest.Tracker, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		workspaceID := chi.URLParam(r, "workspaceId")
		if _, ok := requireWorkspaceOwnership(w, r, workspaces, claims.UserID, workspaceID); !ok {
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Streaming is not supported by this server.",
				"code":  "internal_error",
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		ticker := time.NewTicker(ingestStreamPollInterval)
		defer ticker.Stop()

		for {
			event, err := buildIngestStreamEvent(r.Context(), datasets, tracker, workspaceID)
			if err != nil {
				logger.Error("building ingest stream event", slog.String("error", err.Error()))
				return
			}

			if err := writeSSEEvent(w, event); err != nil {
				// The client disconnected (tab closed, navigated away) — not an error worth logging.
				// Ingest itself is unaffected: it runs in its own goroutine, rooted in
				// context.Background(), independent of this handler's request context.
				return
			}
			flusher.Flush()

			if event.Done {
				return
			}

			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	}
}

// buildIngestStreamEvent computes the current aggregate ingest state for workspaceID.
func buildIngestStreamEvent(ctx context.Context, datasets IngestDatasetStore, tracker *ingest.Tracker, workspaceID string) (ingestStreamEvent, error) {
	inProgress, err := datasets.ListInProgressByWorkspace(ctx, workspaceID)
	if err != nil {
		return ingestStreamEvent{}, fmt.Errorf("listing in-progress datasets: %w", err)
	}

	if len(inProgress) == 0 {
		// Nothing pending, provisioning, profiling, or failed for this workspace — either nothing has
		// ever been uploaded, or everything already reached "complete" in a later wave. Either way,
		// there is nothing left for this screen to show.
		return ingestStreamEvent{Datasets: []publicIngestDataset{}, OverallStep: string(ingest.StepProfiling), Done: true}, nil
	}

	event := ingestStreamEvent{Datasets: make([]publicIngestDataset, 0, len(inProgress))}

	overall := ingest.StepRelationships // start at the most-advanced step; narrow down below
	anyFailed := false
	allHandedOff := true

	for _, ds := range inProgress {
		state := ingest.StateFor(tracker, ds)

		event.Datasets = append(event.Datasets, publicIngestDataset{
			ID:       ds.ID,
			Filename: ds.OriginalFilename,
			Step:     string(state.Step),
			Error:    state.Error,
		})

		if state.Step == ingest.StepFailed {
			anyFailed = true
			continue
		}
		if state.Step != ingest.StepProfiling && state.Step != ingest.StepRelationships {
			allHandedOff = false
		}
		if stepOrder[state.Step] < stepOrder[overall] {
			overall = state.Step
		}
	}

	if anyFailed {
		event.OverallStep = string(ingest.StepFailed)
		event.Done = true
		return event, nil
	}

	event.OverallStep = string(overall)
	// Done once FEAT-004 has handed every dataset in the batch off to profiling — this package's own
	// scope ends there; it does not wait for FEAT-005/FEAT-008 to actually run.
	event.Done = allHandedOff
	return event, nil
}

// writeSSEEvent writes one `data: <json>\n\n` frame, the minimal well-formed SSE message EventSource
// on the client (web/src/lib/api-client.ts's subscribeToStream) expects.
func writeSSEEvent(w http.ResponseWriter, event ingestStreamEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling ingest stream event: %w", err)
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}
