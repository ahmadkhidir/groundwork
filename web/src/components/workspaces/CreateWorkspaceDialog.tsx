"use client";

import { useId, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Card } from "@/components/ui/Card";

export interface CreateWorkspaceDialogProps {
  onCreate: (name: string) => Promise<void>;
  onClose: () => void;
}

/**
 * The workspace-name prompt opened by "Create Workspace" (web button) / the FAB (mobile) — screens/
 * 03-workspace-list.md's Create Workspace component.
 *
 * Built as a self-contained overlay rather than through a shared `Modal` primitive: `web/src/
 * components/ui/` (where one would belong) is read-only to this wave, and no Modal exists there yet
 * — screens.md's Delete Confirmation Modal will need the same primitive. Reported as a needed
 * addition for whichever wave builds workspace settings/deletion (FEAT-043).
 */
export function CreateWorkspaceDialog({
  onCreate,
  onClose,
}: CreateWorkspaceDialogProps) {
  const titleId = useId();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;

    const trimmed = name.trim();
    if (!trimmed) {
      setError("Workspace name is required.");
      return;
    }

    setError(null);
    setSubmitting(true);
    try {
      await onCreate(trimmed);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Something went wrong. Try again.");
      setSubmitting(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-text-primary/40 p-4"
      onClick={onClose}
      role="presentation"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-sm"
      >
        <Card elevation="md">
          <h2 id={titleId} className="text-h3 text-text-primary">
            Create workspace
          </h2>
          <form onSubmit={handleSubmit} className="mt-4 flex flex-col gap-4">
            <Input
              label="Workspace name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              error={error ?? undefined}
              disabled={submitting}
              required
            />
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose} disabled={submitting}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" loading={submitting}>
                Create
              </Button>
            </div>
          </form>
        </Card>
      </div>
    </div>
  );
}
