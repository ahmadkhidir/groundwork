/**
 * Live ingest step, mirroring the Go API's internal/ingest.Step plus the fixed "failed" terminal
 * value — see server/internal/api/ingest.go's ingestStreamEvent.
 *
 * "provisioning" and "loading" are driven by FEAT-004 (this wave). "profiling" and "relationships"
 * exist here because screens/06-ingest-progress.md's step indicator shows all four steps, but they
 * are NOT driven yet — FEAT-005 (profiling) and FEAT-008 (relationship inference) land in Wave 4.
 * Reaching "profiling" on this screen right now means "FEAT-004 finished successfully," not "the
 * whole pipeline finished" — an honest reflection of what has actually shipped, not a fabricated
 * further-along state.
 */
export type IngestStep = "provisioning" | "loading" | "profiling" | "relationships" | "failed";

/** One dataset's live ingest state, as sent by GET /v1/workspaces/:id/ingest/stream. */
export interface IngestDatasetState {
  id: string;
  filename: string;
  step: IngestStep;
  /** Specific, actionable failure reason. Present only when step is "failed". */
  error?: string;
}

/** One frame of the ingest SSE stream — the whole batch's current state. */
export interface IngestStreamEvent {
  datasets: IngestDatasetState[];
  /** The least-advanced step across the batch, or "failed" if any dataset failed. */
  overall_step: IngestStep;
  /**
   * True once every dataset in the batch has been handed off past this wave's scope (or failed).
   * See the server-side ingestStreamEvent.Done doc comment for why this does not wait for
   * profiling/relationship-inference to actually finish in this wave.
   */
  done: boolean;
}
