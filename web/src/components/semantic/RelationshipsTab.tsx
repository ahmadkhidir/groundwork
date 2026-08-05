"use client";

import { useState, type ReactNode } from "react";
import { Network, Pencil } from "lucide-react";
import { EmptyState } from "@/components/shared/EmptyState";
import { Button } from "@/components/ui/Button";
import type { ConfirmationColumn } from "@/components/confirmation/types";
import type { Cardinality } from "@/types/schema";
import { RelationshipEditor } from "./RelationshipEditor";
import type { SemanticRelationship } from "./types";

export interface RelationshipsTabProps {
  relationships: SemanticRelationship[];
  columnsById: Map<string, ConfirmationColumn>;
  savingIds: Set<string>;
  errors: Record<string, string>;
  onUpdate: (id: string, cardinality: Cardinality, grainNotes: string) => void;
  /** True for a read-only shared member (FEAT-045) — no Edit action rendered. Always false this wave. */
  readOnly?: boolean;
}

function columnLabel(columnsById: Map<string, ConfirmationColumn>, columnId: string): string {
  const col = columnsById.get(columnId);
  if (!col) return columnId;
  return `${col.dataset_filename}: ${col.name}`;
}

function cardinalityLabel(cardinality: Cardinality): string {
  return cardinality.replace(/_/g, " ");
}

/**
 * FEAT-019's Relationships tab (screens/15-semantic-layer.md) — the read/edit surface over the same
 * `relationships` rows FEAT-009 seeds on candidate-join accept.
 *
 * Renders only the fields schema.md's Relationship entity actually carries (source/target column,
 * cardinality, grain notes, confirmed_at) — see types.ts's doc comment on the confidence/status
 * fields screens/15 describes that this entity does not have.
 */
export function RelationshipsTab({
  relationships,
  columnsById,
  savingIds,
  errors,
  onUpdate,
  readOnly = false,
}: RelationshipsTabProps) {
  const [editingId, setEditingId] = useState<string | null>(null);

  if (relationships.length === 0) {
    return (
      <EmptyState
        icon={Network}
        title="No relationships yet"
        subtitle="Confirmed joins between your uploaded files will appear here once you accept a candidate join during ingest confirmation."
      />
    );
  }

  function renderEditOrRow(rel: SemanticRelationship, asRow: () => ReactNode) {
    if (editingId === rel.id) {
      return (
        <RelationshipEditor
          cardinality={rel.cardinality}
          grainNotes={rel.grain_notes ?? ""}
          saving={savingIds.has(rel.id)}
          error={errors[rel.id]}
          onSave={(cardinality, grainNotes) => onUpdate(rel.id, cardinality, grainNotes)}
          onCancel={() => setEditingId(null)}
        />
      );
    }
    return asRow();
  }

  return (
    <>
      {/* Mobile: stacked list. */}
      <ul className="flex flex-col gap-3 md:hidden">
        {relationships.map((rel) => (
          <li key={rel.id} className="rounded-md border border-border p-3">
            {renderEditOrRow(rel, () => (
              <>
                <div className="mb-1 flex items-center justify-between gap-2">
                  <span className="font-medium text-text-primary">
                    {columnLabel(columnsById, rel.source_column_id)} &harr;{" "}
                    {columnLabel(columnsById, rel.target_column_id)}
                  </span>
                  {!readOnly && (
                    <Button variant="ghost" touchTarget onClick={() => setEditingId(rel.id)} aria-label="Edit relationship">
                      <Pencil size={16} aria-hidden="true" />
                    </Button>
                  )}
                </div>
                <p className="text-body-small text-text-secondary">
                  {cardinalityLabel(rel.cardinality)}
                  {rel.grain_notes ? ` — ${rel.grain_notes}` : ""}
                </p>
              </>
            ))}
          </li>
        ))}
      </ul>

      {/* Web: dense table. */}
      <div className="hidden md:block">
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
              <th className="px-2 py-2 font-medium">Source column</th>
              <th className="px-2 py-2 font-medium">Target column</th>
              <th className="px-2 py-2 font-medium">Cardinality</th>
              <th className="px-2 py-2 font-medium">Grain notes</th>
              {!readOnly && <th className="px-2 py-2 font-medium">Edit</th>}
            </tr>
          </thead>
          <tbody>
            {relationships.map((rel) => (
              <tr key={rel.id} className="border-b border-border last:border-b-0 align-top">
                {editingId === rel.id ? (
                  <td className="px-2 py-2" colSpan={readOnly ? 4 : 5}>
                    {renderEditOrRow(rel, () => null)}
                  </td>
                ) : (
                  <>
                    <td className="px-2 py-2 font-medium text-text-primary">
                      {columnLabel(columnsById, rel.source_column_id)}
                    </td>
                    <td className="px-2 py-2 font-medium text-text-primary">
                      {columnLabel(columnsById, rel.target_column_id)}
                    </td>
                    <td className="px-2 py-2 text-text-primary">{cardinalityLabel(rel.cardinality)}</td>
                    <td className="px-2 py-2 text-text-secondary">{rel.grain_notes || "—"}</td>
                    {!readOnly && (
                      <td className="px-2 py-2">
                        <Button variant="ghost" onClick={() => setEditingId(rel.id)}>
                          <Pencil size={16} aria-hidden="true" />
                          Edit
                        </Button>
                      </td>
                    )}
                  </>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
