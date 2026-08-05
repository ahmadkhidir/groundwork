/**
 * Product limits and tunable thresholds, client side.
 *
 * These pair with server/internal/config in the Go service. The SERVER is authoritative: every
 * limit here is a UX affordance that fails fast and explains itself, never a control. A client that
 * skips validation still hits the same rejection on the server.
 *
 * Keeping them aligned is a build concern, not a runtime one — the values below read from
 * NEXT_PUBLIC_* env vars with the documented defaults, so a single deployment config feeds both
 * services. See IMPLEMENTATION.md §5 rule 12 for the threshold table and its sources.
 */

/**
 * Maximum size of a single uploaded file, in bytes. Default 500 MB.
 *
 * Confirmed by the product owner 2026-08-05; features.md FEAT-004 specifies only "a stated size
 * ceiling" without a figure. Applies PER FILE and across ALL tiers — monetization.md gates usage
 * allotments (engine-seconds, storage, model tokens), not per-file size.
 */
export const MAX_UPLOAD_BYTES: number = Number(
  process.env.NEXT_PUBLIC_MAX_UPLOAD_BYTES ?? 500 * 1024 * 1024,
);

/** File formats accepted at upload, matching the `DatasetFormat` enum in schema.md. */
export const ACCEPTED_UPLOAD_EXTENSIONS = [
  ".csv",
  ".xlsx",
  ".xls",
  ".parquet",
  ".sqlite",
  ".db",
] as const;

/**
 * Formats a byte count for display using binary units.
 *
 * Used both in the upload rejection message and in the selected-file list, so a user comparing
 * "your file" against "the limit" sees the two rendered the same way.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${bytes} B`;

  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unitIndex = 0;

  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }

  // One decimal below 10 (9.4 MB), none above (412 MB) — precision where it aids comparison.
  const rendered = value < 10 ? value.toFixed(1) : Math.round(value).toString();
  return `${rendered} ${units[unitIndex]}`;
}

/** The upload ceiling rendered for display, e.g. "500 MB". */
export function maxUploadSizeLabel(): string {
  return formatBytes(MAX_UPLOAD_BYTES);
}

/** Outcome of validating one selected file before upload begins. */
export interface FileValidationResult {
  valid: boolean;
  /**
   * Rejection reason, ready to display. Empty when `valid` is true.
   *
   * screens/05-file-upload.md is explicit that an oversized file is rejected "with the specific
   * ceiling stated, not a generic 'file too large'" — so the message always names both the file's
   * size and the limit.
   */
  message: string;
}

/**
 * Validates one file's type and size before upload starts.
 *
 * Runs client-side purely so the user learns immediately rather than after uploading 500 MB. The
 * server repeats both checks on receipt.
 */
export function validateUploadFile(file: {
  name: string;
  size: number;
}): FileValidationResult {
  const lowerName = file.name.toLowerCase();
  const hasAcceptedExtension = ACCEPTED_UPLOAD_EXTENSIONS.some((extension) =>
    lowerName.endsWith(extension),
  );

  if (!hasAcceptedExtension) {
    return {
      valid: false,
      message: `${file.name} is not a supported format. Upload a CSV, Excel, Parquet, or SQLite file.`,
    };
  }

  if (file.size > MAX_UPLOAD_BYTES) {
    return {
      valid: false,
      message: `${file.name} is ${formatBytes(file.size)}, which is over the ${maxUploadSizeLabel()} limit for a single file.`,
    };
  }

  return { valid: true, message: "" };
}
