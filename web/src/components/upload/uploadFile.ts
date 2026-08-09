import { apiUrl, getSessionTokenForTransport } from "@/lib/api-client";
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
 *
 * Production auth is the `Authorization: Bearer` header (the Vercel web and Fly API share no cookie
 * domain, so `withCredentials` alone cannot authenticate), and the fetch-based `api` helper attaches
 * it automatically — but this upload bypasses that helper, so the header is attached here from the
 * same BFF-issued token. A 401 is NOT retried: with a fresh token attached it means the session is
 * genuinely invalid (secret rotation or expiry), and re-sending a potentially large file on a
 * speculative retry is worse than surfacing the failure.
 */
export function uploadFile(
  workspaceId: string,
  file: File,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<UploadFileResult> {
  return uploadWithToken(workspaceId, file, onProgress, signal);
}

async function uploadWithToken(
  workspaceId: string,
  file: File,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<UploadFileResult> {
  const token = await getSessionTokenForTransport();

  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", apiUrl(`/v1/workspaces/${workspaceId}/files`));
    if (token) {
      xhr.setRequestHeader("Authorization", `Bearer ${token}`);
    }
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
