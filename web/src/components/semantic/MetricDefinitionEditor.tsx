"use client";

import { useId, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Card } from "@/components/ui/Card";
import type { SemanticMetricDefinition } from "./types";

export interface MetricDefinitionEditorProps {
  /** Omit to create a new metric definition; pass an existing one to edit it in place. */
  metricDefinition?: SemanticMetricDefinition;
  onSave: (fields: { name: string; sqlExpression: string; description: string }) => Promise<void>;
  onClose: () => void;
}

/**
 * The Metric Definitions tab's inline/modal editor (screens/15-semantic-layer.md: "Editing...
 * Inline edit form/modal, Save/Cancel actions"), used for both creating a new metric definition and
 * editing an existing one — see semantic.go's doc comment on `createMetricDefinitionRequest` for why
 * creation is in scope this wave.
 *
 * Built as a self-contained overlay rather than through a shared `Modal` primitive, following the
 * same precedent as CreateWorkspaceDialog.tsx: `web/src/components/ui/` has no Modal component yet.
 * Reported there already; not re-reported here to avoid duplicate noise.
 */
export function MetricDefinitionEditor({
  metricDefinition,
  onSave,
  onClose,
}: MetricDefinitionEditorProps) {
  const titleId = useId();
  const [name, setName] = useState(metricDefinition?.name ?? "");
  const [sqlExpression, setSqlExpression] = useState(metricDefinition?.sql_expression ?? "");
  const [description, setDescription] = useState(metricDefinition?.description ?? "");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const isEditing = Boolean(metricDefinition);
  const sqlFieldId = useId();
  const descriptionFieldId = useId();

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;

    const trimmedName = name.trim();
    const trimmedSql = sqlExpression.trim();
    if (!trimmedName || !trimmedSql) {
      setError("A metric definition needs both a name and a SQL expression.");
      return;
    }

    setError(null);
    setSubmitting(true);
    try {
      await onSave({ name: trimmedName, sqlExpression: trimmedSql, description: description.trim() });
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
        className="w-full max-w-lg"
      >
        <Card elevation="md">
          <h2 id={titleId} className="text-h3 text-text-primary">
            {isEditing ? "Edit metric definition" : "New metric definition"}
          </h2>
          <form onSubmit={handleSubmit} className="mt-4 flex flex-col gap-4">
            <Input
              label="Name"
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={submitting}
              placeholder="e.g. revenue"
              required
            />

            <div className="flex flex-col gap-1">
              <label htmlFor={sqlFieldId} className="text-body text-text-primary">
                SQL expression
              </label>
              <textarea
                id={sqlFieldId}
                value={sqlExpression}
                onChange={(e) => setSqlExpression(e.target.value)}
                disabled={submitting}
                rows={3}
                placeholder="SUM(total) FILTER (WHERE status != 'cancelled')"
                className="rounded-md border border-border bg-surface px-2 py-1 font-mono text-body-small text-text-primary"
                required
              />
            </div>

            <div className="flex flex-col gap-1">
              <label htmlFor={descriptionFieldId} className="text-body text-text-primary">
                Description
              </label>
              <textarea
                id={descriptionFieldId}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                disabled={submitting}
                rows={2}
                placeholder="What this metric means, and why it's defined this way."
                className="rounded-md border border-border bg-surface px-2 py-1 text-body text-text-primary"
              />
            </div>

            {error && (
              <p role="alert" className="text-body-small text-error">
                {error}
              </p>
            )}

            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose} disabled={submitting}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" loading={submitting}>
                Save
              </Button>
            </div>
          </form>
        </Card>
      </div>
    </div>
  );
}
