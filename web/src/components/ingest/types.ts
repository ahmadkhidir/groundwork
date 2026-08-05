/**
 * Live ingest step, mirroring the Go API's internal/ingest.Step plus the fixed "failed" terminal
 * value — see server/internal/api/ingest.go's ingestStreamEvent.
 *
 * "provisioning" and "loading" are driven by FEAT-004. "profiling" (FEAT-005) and "relationships"
 * (FEAT-008, Wave 4) are now real, actively-running steps too — reaching them means the platform is
 * genuinely computing column statistics or candidate joins, not merely that an earlier step handed
 * off. See `done` below for what "the whole pipeline finished" actually means as of Wave 4.
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
  /** The least-advanced step across datasets still in progress, or "failed" if any dataset failed. */
  overall_step: IngestStep;
  /**
   * Every dataset ever uploaded to this workspace, regardless of status — the count screens/06's
   * "Finding relationships (if 2+ files)" condition is evaluated against. NOT the same as
   * `datasets.length`, which only reflects datasets still in progress and would under-count (and
   * flicker the relationships step's visibility) the moment any one file in a multi-file batch
   * reaches "complete" before its siblings do.
   */
  total_datasets: number;
  /**
   * True once every dataset in the workspace has reached a genuinely terminal state: fully
   * "complete" (profiling, and relationship inference when applicable, both finished) or "failed".
   */
  done: boolean;
}
