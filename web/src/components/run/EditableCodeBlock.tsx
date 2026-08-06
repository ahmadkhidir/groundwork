"use client";

import { useState } from "react";
import { Button } from "@/components/ui/Button";

export interface EditableCodeBlockProps {
  /** Visible heading above the block, e.g. "Generated SQL". */
  label: string;
  /** The SQL text shown and edited. */
  code: string;
  /** Whether the code may be edited at all (web-only per screens/08-run-viewer.md). */
  editable?: boolean;
  /** Caption under the block, e.g. "Runs exactly this SQL in a new run". */
  hint?: string;
  /** Fires with the edited SQL when the user commits a change. */
  onRunEdited?: (sql: string) => void;
  /** Busy state while the edited query is being submitted. */
  submitting?: boolean;
}

/**
 * FEAT-018's SQL editing surface (screens/08-run-viewer.md "SQL / Code Block", edit: web only).
 *
 * The Run Viewer's generated SQL becomes an editable, mono-spaced text area with a "Run edited
 * query" action. Per IMPLEMENTATION.md's Phase 2 boundary, editing never mutates the current run —
 * it starts a NEW run seeded with the edited SQL (the server's SubmitWithSQL path), so the edited
 * query is traceable as its own run, not silently folded into the original.
 *
 * Editing is deliberately opt-in: the block reads as code until "Edit" is pressed, so the viewer
 * never surprises a watch-only user with a textarea.
 */
export function EditableCodeBlock({
  label,
  code,
  editable = false,
  hint,
  onRunEdited,
  submitting = false,
}: EditableCodeBlockProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(code);

  if (!editable || !onRunEdited) {
    return (
      <div className="flex flex-col gap-2">
        <div className="flex items-baseline justify-between gap-2">
          <h3 className="text-h3 text-text-primary">{label}</h3>
          {hint && <p className="text-body-small text-text-secondary">{hint}</p>}
        </div>
        {code.length > 0 ? (
          <pre
            className="overflow-x-auto rounded-md border border-border bg-surface-secondary p-3 text-body-small"
            data-testid="code-block"
          >
            <code className="block font-mono text-text-primary whitespace-pre">{code}</code>
          </pre>
        ) : (
          <p className="rounded-md border border-border bg-surface-secondary p-3 text-body-small text-text-secondary">
            No SQL generated yet.
          </p>
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between gap-2">
        <h3 className="text-h3 text-text-primary">{label}</h3>
        {hint && <p className="text-body-small text-text-secondary">{hint}</p>}
      </div>

      {editing ? (
        <>
          <textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            rows={Math.max(4, Math.min(16, draft.split("\n").length + 1))}
            spellCheck={false}
            aria-label={`Edit ${label}`}
            className="rounded-md border border-border bg-surface-secondary p-3 font-mono text-body-small text-text-primary focus:border-primary focus:outline-none"
          />
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              loading={submitting}
              onClick={() => onRunEdited(draft)}
            >
              Run edited query
            </Button>
            <Button
              variant="ghost"
              disabled={submitting}
              onClick={() => {
                setDraft(code);
                setEditing(false);
              }}
            >
              Cancel
            </Button>
          </div>
        </>
      ) : (
        <>
          <pre
            className="overflow-x-auto rounded-md border border-border bg-surface-secondary p-3 text-body-small"
            data-testid="code-block"
          >
            <code className="block font-mono text-text-primary whitespace-pre">{code}</code>
          </pre>
          <div>
            <Button variant="outline" onClick={() => setEditing(true)}>
              Edit SQL
            </Button>
          </div>
        </>
      )}
    </div>
  );
}