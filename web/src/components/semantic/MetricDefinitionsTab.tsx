"use client";

import { useState } from "react";
import { Calculator, Pencil, Plus, Trash2 } from "lucide-react";
import { EmptyState } from "@/components/shared/EmptyState";
import { Button } from "@/components/ui/Button";
import { MetricDefinitionEditor } from "./MetricDefinitionEditor";
import type { SemanticMetricDefinition } from "./types";

export interface MetricDefinitionsTabProps {
  metricDefinitions: SemanticMetricDefinition[];
  savingIds: Set<string>;
  onCreate: (fields: { name: string; sqlExpression: string; description: string }) => Promise<void>;
  onUpdate: (id: string, fields: { name: string; sqlExpression: string; description: string }) => Promise<void>;
  onDelete: (id: string) => void;
  /** True for a read-only shared member (FEAT-045) — no Edit/Delete/New actions rendered. Always false this wave. */
  readOnly?: boolean;
}

function formatTimestamp(iso: string): string {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? iso : parsed.toLocaleString();
}

/**
 * FEAT-019's Metric Definitions tab (screens/15-semantic-layer.md) — full CRUD over named business
 * metrics, per this wave's explicit scope grant.
 */
export function MetricDefinitionsTab({
  metricDefinitions,
  savingIds,
  onCreate,
  onUpdate,
  onDelete,
  readOnly = false,
}: MetricDefinitionsTabProps) {
  const [editorTarget, setEditorTarget] = useState<"new" | SemanticMetricDefinition | null>(null);

  const newButton = !readOnly && (
    <Button variant="secondary" onClick={() => setEditorTarget("new")}>
      <Plus size={16} aria-hidden="true" />
      New metric
    </Button>
  );

  return (
    <div className="flex flex-col gap-4">
      {!readOnly && <div className="flex justify-end">{newButton}</div>}

      {metricDefinitions.length === 0 ? (
        // No ctaLabel/onCta here: the "New metric" button above already covers this action, and
        // EmptyState's own CTA would otherwise render a second, identically-labelled button.
        <EmptyState
          icon={Calculator}
          title="No metric definitions yet"
          subtitle="Define what a term like “revenue” means for this workspace so every question answers it consistently."
        />
      ) : (
        <>
          {/* Mobile: stacked list. */}
          <ul className="flex flex-col gap-3 md:hidden">
            {metricDefinitions.map((m) => (
              <li key={m.id} className="rounded-md border border-border p-3">
                <div className="mb-1 flex items-center justify-between gap-2">
                  <span className="font-medium text-text-primary">{m.name}</span>
                  {!readOnly && (
                    <div className="flex gap-1">
                      <Button variant="ghost" touchTarget onClick={() => setEditorTarget(m)} aria-label={`Edit ${m.name}`}>
                        <Pencil size={16} aria-hidden="true" />
                      </Button>
                      <Button
                        variant="ghost"
                        touchTarget
                        loading={savingIds.has(m.id)}
                        onClick={() => onDelete(m.id)}
                        aria-label={`Delete ${m.name}`}
                      >
                        <Trash2 size={16} aria-hidden="true" />
                      </Button>
                    </div>
                  )}
                </div>
                <code className="block whitespace-pre-wrap font-mono text-body-small text-code-accent">
                  {m.sql_expression}
                </code>
                {m.description && <p className="mt-1 text-body-small text-text-secondary">{m.description}</p>}
                <p className="mt-1 text-caption text-text-secondary">Last modified {formatTimestamp(m.updated_at)}</p>
              </li>
            ))}
          </ul>

          {/* Web: dense table. */}
          <div className="hidden md:block">
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
                  <th className="px-2 py-2 font-medium">Metric name</th>
                  <th className="px-2 py-2 font-medium">SQL expression</th>
                  <th className="px-2 py-2 font-medium">Last modified</th>
                  {!readOnly && <th className="px-2 py-2 font-medium">Actions</th>}
                </tr>
              </thead>
              <tbody>
                {metricDefinitions.map((m) => (
                  <tr key={m.id} className="border-b border-border align-top last:border-b-0">
                    <td className="px-2 py-2 font-medium text-text-primary">{m.name}</td>
                    <td className="px-2 py-2">
                      <code className="block max-w-md whitespace-pre-wrap font-mono text-body-small text-code-accent">
                        {m.sql_expression}
                      </code>
                    </td>
                    <td className="px-2 py-2 text-body-small text-text-secondary">{formatTimestamp(m.updated_at)}</td>
                    {!readOnly && (
                      <td className="px-2 py-2">
                        <div className="flex gap-2">
                          <Button variant="ghost" onClick={() => setEditorTarget(m)}>
                            <Pencil size={16} aria-hidden="true" />
                            Edit
                          </Button>
                          <Button variant="ghost" loading={savingIds.has(m.id)} onClick={() => onDelete(m.id)}>
                            <Trash2 size={16} aria-hidden="true" />
                            Delete
                          </Button>
                        </div>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      {editorTarget && (
        <MetricDefinitionEditor
          metricDefinition={editorTarget === "new" ? undefined : editorTarget}
          onClose={() => setEditorTarget(null)}
          onSave={async (fields) => {
            if (editorTarget === "new") {
              await onCreate(fields);
            } else {
              await onUpdate(editorTarget.id, fields);
            }
            setEditorTarget(null);
          }}
        />
      )}
    </div>
  );
}
