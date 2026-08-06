/**
 * One row in the File Upload screen's selected-files list, tracking its own upload lifecycle.
 *
 * "checking-duplicate" and "duplicate" back FEAT-049: every selected file is checked against the
 * workspace's existing datasets before it may proceed to "pending" (submittable). A file whose name
 * collides stays "duplicate" — Start Ingest excludes it — until the user explicitly resolves it via
 * the Duplicate Filename dialog (Replace or Keep both), per screens/05-file-upload.md's Data Flow.
 */
export interface SelectedFile {
  id: string;
  file: File;
  status: "checking-duplicate" | "duplicate" | "pending" | "uploading" | "success" | "error";
  /** 0-1, only meaningful while status is "uploading". */
  progress: number;
  errorMessage?: string;
  /** Populated only while status is "duplicate": the existing dataset this file's name collides with. */
  duplicate?: {
    existingDatasetId: string;
  };
}
