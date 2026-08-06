"use client";

import { useEffect, useRef, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/Button";

export interface DeleteConfirmationModalProps {
  open: boolean;
  title: string;
  /**
   * The explicit scope of what will be destroyed, per screens.md's shared Delete Confirmation Modal
   * pattern — "never a bare 'are you sure?'". Each entry renders as its own bullet.
   */
  items: ReactNode[];
  /**
   * A second, visually distinct warning for review work (FEAT-006 type overrides, FEAT-007 PII
   * decisions) that is lost alongside the items above and is NOT carried forward to a future
   * re-upload — screens/04-workspace-home.md and screens/05-file-upload.md both require this to be
   * stated separately from the dependency list, not folded into it.
   */
  reviewWorkWarning?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  onCancel: () => void;
  onConfirm: () => void;
  loading?: boolean;
  /** Server-side failure from a prior confirm attempt — shown inline so the user can retry. */
  error?: string;
}

/**
 * The shared Delete Confirmation Modal (screens.md's Shared/Common Components): explicit scope of
 * what will be destroyed spelled out in the body, Ghost "Cancel" and Destructive "Delete" per
 * branding.md's Buttons component tokens.
 *
 * Used by FEAT-048 (single-dataset deletion, screens/04-workspace-home.md) and FEAT-049's Replace
 * choice (screens/05-file-upload.md), which reuses this exact pattern rather than a bespoke dialog —
 * per that screen's own spec. Also the pattern screens/14-deletion-confirmation-proof.md (FEAT-043,
 * not yet built) extends for whole-workspace deletion.
 */
export function DeleteConfirmationModal({
  open,
  title,
  items,
  reviewWorkWarning,
  confirmLabel = "Delete",
  cancelLabel = "Cancel",
  onCancel,
  onConfirm,
  loading = false,
  error,
}: DeleteConfirmationModalProps) {
  const confirmButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    confirmButtonRef.current?.focus();

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") onCancel();
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, onCancel]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-text-primary/40 p-4"
      onClick={onCancel}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-confirmation-title"
        className="w-full max-w-md rounded-md border border-border bg-surface p-6 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="mb-3 flex items-start gap-3">
          <AlertTriangle size={24} className="mt-0.5 shrink-0 text-error" aria-hidden="true" />
          <h2 id="delete-confirmation-title" className="text-h3 text-text-primary">
            {title}
          </h2>
        </div>

        <ul className="mb-3 list-disc space-y-1 pl-9 text-body-small text-text-secondary">
          {items.map((item, i) => (
            <li key={i}>{item}</li>
          ))}
        </ul>

        {reviewWorkWarning && (
          <p className="mb-4 rounded-md border border-warning/40 bg-warning/12 p-3 text-body-small text-text-primary">
            {reviewWorkWarning}
          </p>
        )}

        {error && (
          <p role="alert" className="mb-4 text-body-small text-error">
            {error}
          </p>
        )}

        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={onCancel} disabled={loading}>
            {cancelLabel}
          </Button>
          <Button
            ref={confirmButtonRef}
            variant="destructive"
            onClick={onConfirm}
            loading={loading}
          >
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
