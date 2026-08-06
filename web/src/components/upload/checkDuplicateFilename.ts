import { api } from "@/lib/api-client";

interface DuplicateCheckResponse {
  exists: boolean;
  dataset?: { id: string; original_filename: string; ingest_status: string; created_at: string };
}

/**
 * Checks GET /v1/workspaces/:id/files?filename= — FEAT-049's pre-upload collision check. Matches on
 * exact original_filename, regardless of the existing dataset's ingest_status (including "failed",
 * the retry case that caused the real incident this feature exists to prevent).
 */
export async function checkDuplicateFilename(
  workspaceId: string,
  filename: string,
): Promise<{ exists: boolean; existingDatasetId?: string }> {
  const res = await api.get<DuplicateCheckResponse>(
    `/v1/workspaces/${workspaceId}/files?filename=${encodeURIComponent(filename)}`,
  );
  return { exists: res.exists, existingDatasetId: res.dataset?.id };
}
