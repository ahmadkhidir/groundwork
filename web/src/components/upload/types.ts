/** One row in the File Upload screen's selected-files list, tracking its own upload lifecycle. */
export interface SelectedFile {
  id: string;
  file: File;
  status: "pending" | "uploading" | "success" | "error";
  /** 0-1, only meaningful while status is "uploading". */
  progress: number;
  errorMessage?: string;
}
