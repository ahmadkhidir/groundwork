"use client";

import { useEffect, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { api, ApiError } from "@/lib/api-client";

export interface AccountDeleteModalProps {
  /** The account email the user must type to confirm — the type-to-confirm gate. */
  email: string;
  onClose: () => void;
  /** Fired after the account cascade completes server-side. */
  onDeleted: () => void;
}

type DeleteState =
  | { status: "idle" }
  | { status: "deleting" }
  | { status: "partial_failed"; message: string }
  | { status: "error"; message: string };

/**
 * FEAT-052's account-deletion confirmation — the shared Delete Confirmation Modal pattern with a
 * type-to-confirm gate (the account email, mirroring FEAT-043's workspace-name typing). Explicit
 * scope bullets before commit, honest Deleting state, and a partial-failure state that names the
 * failure and keeps the account signed in and intact (never a false success).
 *
 * The parent renders this ONLY while open, so a fresh mount is a fresh confirmation (typed text and
 * state reset by construction, no reset effect needed).
 */
export function AccountDeleteModal({ email, onClose, onDeleted }: AccountDeleteModalProps) {
  const [typed, setTyped] = useState("");
  const [state, setState] = useState<DeleteState>({ status: "idle" });
  const confirmButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    confirmButtonRef.current?.focus();

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") onClose();
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  const matches = typed === email;
  const canConfirm = matches && state.status !== "deleting";

  const confirm = async () => {
    if (!canConfirm) return;
    setState({ status: "deleting" });
    try {
      await api.delete("/v1/account");
      onDeleted();
    } catch (error) {
      if (error instanceof ApiError && error.code === "account_deletion_partial_failed") {
        setState({
          status: "partial_failed",
          message: "The deletion did not complete. Your account is intact — retry to continue.",
        });
      } else {
        setState({
          status: "error",
          message:
            error instanceof ApiError ? error.message : "Something went wrong. Try again.",
        });
      }
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-text-primary/40 p-4"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-delete-title"
        className="w-full max-w-md rounded-md border border-border bg-surface p-6 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="mb-3 flex items-start gap-3">
          <AlertTriangle size={24} className="mt-0.5 shrink-0 text-error" aria-hidden="true" />
          <h2 id="account-delete-title" className="text-h3 text-text-primary">
            Delete your account?
          </h2>
        </div>

        <ul className="mb-3 list-disc space-y-1 pl-9 text-body-small text-text-secondary">
          <li>Every workspace you own and all of its data — destroyed, with a deletion proof kept</li>
          <li>Your read-only memberships in other people&apos;s workspaces — removed</li>
          <li>Your paid plan cancelled immediately, your billing details deleted</li>
          <li>No automatic refund — this cannot be undone</li>
        </ul>

        <label className="mt-4 block text-body-small text-text-secondary" htmlFor="account-delete-confirm">
          Type <span className="font-medium text-text-primary">{email}</span> to confirm:
        </label>
        <input
          id="account-delete-confirm"
          type="text"
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          disabled={state.status === "deleting"}
          className="mt-1 w-full rounded-md border border-border bg-surface-secondary px-3 py-2 text-body text-text-primary focus:border-primary focus:outline-none"
          placeholder={email}
        />

        {state.status === "deleting" && (
          <p role="status" className="mt-3 text-body-small text-text-secondary">
            Deleting your account — this is permanent…
          </p>
        )}
        {state.status === "partial_failed" && (
          <p role="alert" className="mt-3 text-body-small text-error">
            {state.message}
          </p>
        )}
        {state.status === "error" && (
          <p role="alert" className="mt-3 text-body-small text-error">
            {state.message}
          </p>
        )}

        <div className="mt-4 flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={state.status === "deleting"}>
            Cancel
          </Button>
          <Button
            ref={confirmButtonRef}
            variant="destructive"
            onClick={() => void confirm()}
            disabled={!canConfirm}
            loading={state.status === "deleting"}
          >
            Delete account
          </Button>
        </div>
      </div>
    </div>
  );
}
