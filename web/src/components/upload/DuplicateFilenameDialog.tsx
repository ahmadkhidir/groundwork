"use client";

import { useEffect, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { api, ApiError } from "@/lib/api-client";
import { buildDeletionDisclosure, type DeletionImpact } from "@/components/workspace/DatasetsPanel";

export interface DuplicateFilenameDialogProps {
  workspaceId: string;
  filename: string;
  existingDatasetId: string;
  onResolved: (resolution: "replace" | "keep_both") => void;
  onCancel: () => void;
}

type Choice = "replace" | "keep_both";

/**
 * screens/05-file-upload.md's Duplicate Filename dialog — FEAT-049. Shown per colliding filename
 * before ingest begins. Replace is pre-selected (per that screen's spec) but never auto-applied: the
 * choice always requires an explicit "Continue" click, and reuses the shared Delete Confirmation
 * Modal's disclosure discipline for the Replace path — including the review-work-lost warning FEAT-048
 * requires, since Replace deletes the prior dataset via that same mechanism.
 */
export function DuplicateFilenameDialog({
  workspaceId,
  filename,
  existingDatasetId,
  onResolved,
  onCancel,
}: DuplicateFilenameDialogProps) {
  const [choice, setChoice] = useState<Choice>("replace");
  const [impact, setImpact] = useState<DeletionImpact | null>(null);
  const [loadingImpact, setLoadingImpact] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const continueButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await api.get<DeletionImpact>(
          `/v1/workspaces/${workspaceId}/datasets/${existingDatasetId}/deletion-impact`,
        );
        if (!cancelled) setImpact(res);
      } catch {
        // Non-fatal: Replace can still proceed on the fetched-dataset facts already known (filename)
        // even if the richer impact lookup fails — the disclosure just falls back to the generic
        // review-work warning below rather than blocking the whole dialog on a secondary request.
      } finally {
        if (!cancelled) setLoadingImpact(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [workspaceId, existingDatasetId]);

  useEffect(() => {
    continueButtonRef.current?.focus();
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") onCancel();
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [onCancel]);

  async function handleContinue() {
    if (choice === "keep_both") {
      // "Keep both" never touches the existing dataset — both keep their unmodified
      // original_filename and are disambiguated by created_at everywhere they'd otherwise look
      // identical (screens/07's per-dataset grouping), per FEAT-049.
      onResolved("keep_both");
      return;
    }

    setSubmitting(true);
    setError("");
    try {
      // Replace deletes the prior dataset via FEAT-048's exact mechanism (database rows AND both
      // storage objects), before the new file proceeds to ordinary upload — see
      // UploadScreen.tsx's handleDuplicateResolved.
      await api.delete(`/v1/workspaces/${workspaceId}/datasets/${existingDatasetId}`);
      onResolved("replace");
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Could not replace the existing file. Try again.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  const disclosure = impact ? buildDeletionDisclosure(impact) : null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-text-primary/40 p-4"
      onClick={onCancel}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="duplicate-filename-title"
        className="w-full max-w-md rounded-md border border-border bg-surface p-6 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="mb-3 flex items-start gap-3">
          <AlertTriangle size={24} className="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
          <h2 id="duplicate-filename-title" className="text-h3 text-text-primary">
            &quot;{filename}&quot; already exists in this workspace
          </h2>
        </div>

        <p className="mb-3 text-body-small text-text-secondary">
          Choose whether to replace the existing file or keep both.
        </p>

        <div className="mb-3 flex flex-col gap-2" role="radiogroup" aria-label="Duplicate filename resolution">
          <label
            className={
              "flex cursor-pointer items-start gap-2 rounded-md border p-3 " +
              (choice === "replace" ? "border-primary" : "border-border")
            }
          >
            <input
              type="radio"
              name="duplicate-choice"
              checked={choice === "replace"}
              onChange={() => setChoice("replace")}
              className="mt-1"
            />
            <span>
              <span className="block font-medium text-text-primary">Replace (recommended)</span>
              <span className="block text-body-small text-text-secondary">
                Deletes the existing &quot;{filename}&quot; and uploads this one in its place.
              </span>
            </span>
          </label>
          <label
            className={
              "flex cursor-pointer items-start gap-2 rounded-md border p-3 " +
              (choice === "keep_both" ? "border-primary" : "border-border")
            }
          >
            <input
              type="radio"
              name="duplicate-choice"
              checked={choice === "keep_both"}
              onChange={() => setChoice("keep_both")}
              className="mt-1"
            />
            <span>
              <span className="block font-medium text-text-primary">Keep both</span>
              <span className="block text-body-small text-text-secondary">
                Uploads this file as a separate dataset, distinguished by upload time.
              </span>
            </span>
          </label>
        </div>

        {choice === "replace" &&
          (loadingImpact ? (
            <p className="mb-3 text-body-small text-text-secondary">
              Checking what replacing this file would affect…
            </p>
          ) : disclosure ? (
            <>
              <ul className="mb-3 list-disc space-y-1 pl-6 text-body-small text-text-secondary">
                {disclosure.items.map((item, i) => (
                  <li key={i}>{item}</li>
                ))}
              </ul>
              {disclosure.reviewWorkWarning && (
                <p className="mb-3 rounded-md border border-warning/40 bg-warning/12 p-3 text-body-small text-text-primary">
                  {disclosure.reviewWorkWarning}
                </p>
              )}
            </>
          ) : (
            // The impact lookup itself failed (see the effect above) — Replace can still proceed,
            // but the user is told explicitly rather than shown an empty, reassuring-looking list.
            <p className="mb-3 rounded-md border border-warning/40 bg-warning/12 p-3 text-body-small text-text-primary">
              Replacing this file will permanently delete the existing &quot;{filename}&quot;,
              including any column type corrections and PII review decisions recorded on it. They
              will NOT be carried forward to the new file.
            </p>
          ))}

        {error && (
          <p role="alert" className="mb-3 text-body-small text-error">
            {error}
          </p>
        )}

        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={onCancel} disabled={submitting}>
            Cancel
          </Button>
          <Button
            ref={continueButtonRef}
            variant={choice === "replace" ? "destructive" : "primary"}
            onClick={() => void handleContinue()}
            loading={submitting}
          >
            Continue
          </Button>
        </div>
      </div>
    </div>
  );
}
