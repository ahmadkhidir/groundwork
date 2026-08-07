"use client";

import { useEffect, useRef, useState } from "react";
import { AlertTriangle, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";

export interface WorkspaceDeleteModalProps {
  open: boolean;
  /** The workspace's exact name, used for the type-to-confirm gate (case-sensitive, per screens/14). */
  workspaceName: string;
  onCancel: () => void;
  onConfirm: () => void;
  /** True while the DELETE request is in flight — the Executing state, shown honestly, never a silent wait. */
  loading?: boolean;
  /**
   * Server-side failure from a prior confirm attempt. On a partial cascade failure the server's
   * message names the failed category, so this is shown inline and the user can retry from the same
   * modal (Flow 5 Alternate Path B).
   */
  error?: string;
}

/**
 * The explicit scope of screens/14-deletion-confirmation-proof.md's confirmation modal — the same
 * plain-language enumeration features.md FEAT-043 and user_flow.md Flow 5 require ("object store
 * contents, semantic layer, reports and conversation history, query caches, and logs — not just
 * 'your files'"), never a generic "Are you sure?".
 */
const SCOPE_ITEMS: { label: string; detail: string }[] = [
  { label: "Object store contents", detail: "every uploaded file and normalized dataset" },
  { label: "Semantic layer", detail: "metric definitions, corrections, relationships, columns, and datasets" },
  { label: "Reports and conversation history", detail: "reports, provenance, share links, runs, and steps" },
  { label: "Query caches", detail: "cached deterministic answers" },
  { label: "Logs", detail: "the workspace's audit and model-egress log" },
];

/**
 * The workspace-scoped deletion confirmation modal — screens/14's extension of the shared Delete
 * Confirmation Modal pattern ("extended with explicit scope"): the shared pattern's scope bullets,
 * plus the deliberate high-friction type-the-workspace-name-to-confirm gate that screens/14 requires
 * for an irreversible action.
 *
 * States, per screens/14:
 * - Confirming: modal open, typed name not yet matching — Delete disabled.
 * - Ready to Confirm: typed name matches exactly — Delete enabled.
 * - Executing: cascade in flight — both actions disabled and an honest progress line shown (the
 *   shared honest-status principle: never a silent wait).
 * - Partial/Failed: the server's error message (which names the failed category) is shown inline and
 *   the modal stays open so the user can retry — never a false success.
 *
 * Built in the feature slice rather than extending the shared DeleteConfirmationModal (a shared
 * module) because the type-to-confirm input and the executing-state copy are workspace-deletion
 * specific; the shared component is the pattern this mirrors.
 */
export function WorkspaceDeleteModal({
  open,
  workspaceName,
  onCancel,
  onConfirm,
  loading = false,
  error,
}: WorkspaceDeleteModalProps) {
  const [typedName, setTypedName] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  // Tracks the open transition so the confirmation gate resets ONLY when the modal opens, never on a
  // loading flip mid-execute — otherwise a partial-failure retry (loading true → false) would wipe
  // the user's typed name and silently re-disable Delete.
  const wasOpen = useRef(false);

  const matches = typedName !== "" && typedName === workspaceName;
  const mismatch = typedName !== "" && !matches;

  useEffect(() => {
    if (!open) {
      wasOpen.current = false;
      return;
    }
    if (!wasOpen.current) {
      wasOpen.current = true;
      // Fresh modal, fresh confirmation: the typed name never survives a close, so a re-opened modal
      // cannot pre-enable Delete.
      setTypedName("");
    }
    inputRef.current?.focus();

    function handleKeyDown(event: KeyboardEvent) {
      // Escape never cancels mid-execute — closing the modal while the cascade is running would hide
      // the honest executing state (the cascade itself continues server-side either way).
      if (event.key === "Escape" && !loading) onCancel();
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, loading, onCancel]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-text-primary/40 p-4"
      onClick={() => {
        if (!loading) onCancel();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="workspace-delete-title"
        className="w-full max-w-md rounded-md border border-border bg-surface p-6 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="mb-3 flex items-start gap-3">
          <AlertTriangle size={24} className="mt-0.5 shrink-0 text-error" aria-hidden="true" />
          <h2 id="workspace-delete-title" className="text-h3 text-text-primary">
            Delete workspace
          </h2>
        </div>

        <p className="mb-3 text-body-small text-text-secondary">
          This permanently destroys every piece of derived state in this workspace:
        </p>
        <ul className="mb-4 list-disc space-y-1 pl-9 text-body-small text-text-secondary">
          {SCOPE_ITEMS.map((item) => (
            <li key={item.label}>
              <span className="font-medium text-text-primary">{item.label}</span> — {item.detail}
            </li>
          ))}
        </ul>

        <Input
          ref={inputRef}
          label="Type the workspace name to confirm"
          value={typedName}
          onChange={(event) => setTypedName(event.target.value)}
          error={mismatch ? `The name you typed doesn't match ${workspaceName}.` : undefined}
          hint={`Type ${workspaceName} exactly to enable Delete. This cannot be undone.`}
          disabled={loading}
          autoComplete="off"
          spellCheck={false}
        />

        {error && (
          <p role="alert" className="mt-3 text-body-small text-error">
            {error}
          </p>
        )}

        {loading && (
          <p className="mt-3 flex items-start gap-2 text-body-small text-text-secondary">
            <Loader2 size={16} className="mt-0.5 shrink-0 animate-spin" aria-hidden="true" />
            Deleting your workspace — every category is being destroyed in a single cascade. This may
            take a moment; your workspace remains intact until the cascade completes.
          </p>
        )}

        <div className="mt-5 flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={onCancel} disabled={loading}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={onConfirm}
            loading={loading}
            disabled={!matches}
            data-testid="confirm-workspace-delete"
          >
            Delete workspace
          </Button>
        </div>
      </div>
    </div>
  );
}
