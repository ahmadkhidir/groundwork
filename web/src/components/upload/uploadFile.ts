import { apiUrl } from "@/lib/api-client";
import type { Dataset } from "@/types/schema";

interface UploadFileResult {
  filename: string;
  dataset?: Dataset;
  error?: string;
}

interface UploadResponseBody {
  results: UploadFileResult[];
}

/**
 * Uploads a single file to POST /v1/workspaces/:id/files with progress reporting.
 *
 * Deliberately XMLHttpRequest rather than web/src/lib/api-client.ts's fetch-based `api` helper:
 * `fetch` has no upload-progress event, and screens/05-file-upload.md's Uploading state requires
 * "per-file progress bars" — not achievable with fetch today. One XHR request per file (rather than
 * one multipart request for the whole batch) is also what makes genuinely PER-FILE progress and
 * per-file error isolation possible; the server already accepts either shape.
 */
export function uploadFile(
  workspaceId: string,
  file: File,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<UploadFileResult> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", apiUrl(`/v1/workspaces/${workspaceId}/files`));
    xhr.withCredentials = true;

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        onProgress(event.loaded / event.total);
      }
    };

    xhr.onload = () => {
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(new Error(`Upload failed with status ${xhr.status}`));
        return;
      }
      try {
        const body = JSON.parse(xhr.responseText) as UploadResponseBody;
        const result = body.results[0];
        if (!result) {
          reject(new Error("Upload failed. Try again."));
          return;
        }
        resolve(result);
      } catch {
        reject(new Error("Upload failed. Try again."));
      }
    };

    xhr.onerror = () => reject(new Error("Could not reach Groundwork. Check your connection and try again."));
    xhr.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));

    if (signal) {
      signal.addEventListener("abort", () => xhr.abort());
    }

    const formData = new FormData();
    formData.append("files", file, file.name);
    xhr.send(formData);
  });
}
