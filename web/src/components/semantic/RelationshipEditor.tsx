"use client";

import { useId, useState } from "react";
import { Button } from "@/components/ui/Button";
import type { Cardinality } from "@/types/schema";
import { CARDINALITY_OPTIONS } from "./types";

export interface RelationshipEditorProps {
  cardinality: Cardinality;
  grainNotes: string;
  saving: boolean;
  error?: string;
  onSave: (cardinality: Cardinality, grainNotes: string) => void;
  onCancel: () => void;
}

/**
 * Inline Editing state for a single Relationships-tab row (screens/15-semantic-layer.md: "Editing...
 * Inline edit form/modal, Save/Cancel actions"). Inline rather than a modal — the form is two short
 * fields, and an inline row expansion keeps the surrounding table/list in view.
 */
export function RelationshipEditor({
  cardinality,
  grainNotes,
  saving,
  error,
  onSave,
  onCancel,
}: RelationshipEditorProps) {
  const [draftCardinality, setDraftCardinality] = useState<Cardinality>(cardinality);
  const [draftGrainNotes, setDraftGrainNotes] = useState(grainNotes);
  const selectId = useId();
  const notesId = useId();

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-surface-secondary p-3">
      <div className="flex flex-col gap-1">
        <label htmlFor={selectId} className="text-body-small text-text-secondary">
          Cardinality
        </label>
        <select
          id={selectId}
          value={draftCardinality}
          onChange={(e) => setDraftCardinality(e.target.value as Cardinality)}
          disabled={saving}
          className="h-(--size-input-height) rounded-md border border-border bg-surface px-2 text-body text-text-primary"
        >
          {CARDINALITY_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </div>

      <div className="flex flex-col gap-1">
        <label htmlFor={notesId} className="text-body-small text-text-secondary">
          Grain notes
        </label>
        <textarea
          id={notesId}
          value={draftGrainNotes}
          onChange={(e) => setDraftGrainNotes(e.target.value)}
          disabled={saving}
          rows={2}
          placeholder="e.g. introduces one row per order line, not per order"
          className="rounded-md border border-border bg-surface px-2 py-1 text-body text-text-primary"
        />
      </div>

      {error && (
        <p role="alert" className="text-body-small text-error">
          {error}
        </p>
      )}

      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
        <Button
          variant="primary"
          loading={saving}
          onClick={() => onSave(draftCardinality, draftGrainNotes)}
        >
          Save
        </Button>
      </div>
    </div>
  );
}
