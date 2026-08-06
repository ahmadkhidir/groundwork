import { useState } from "react";
import { AlertCircle } from "lucide-react";
import { InferredType } from "@/types/schema";
import { ConfidenceBadge } from "./ConfidenceBadge";
import {
  confidenceTier,
  datasetGroupLabel,
  groupColumnsByDataset,
  typeFitRate,
  type ConfirmationColumn,
} from "./types";

export interface TypeReviewStepProps {
  columns: ConfirmationColumn[];
  savingIds: Set<string>;
  errors: Record<string, string>;
  onChangeType: (columnId: string, type: string) => void;
}

/** Every value schema.md's Column.inferred_type enum accepts, in a stable review order. */
const TYPE_OPTIONS: { value: string; label: string }[] = [
  { value: InferredType.String, label: "Text" },
  { value: InferredType.Integer, label: "Whole number" },
  { value: InferredType.Number, label: "Decimal number" },
  { value: InferredType.Boolean, label: "True/false" },
  { value: InferredType.Date, label: "Date" },
  { value: InferredType.Timestamp, label: "Date & time" },
];

function typeLabel(value: string): string {
  return TYPE_OPTIONS.find((o) => o.value === value)?.label ?? value;
}

/**
 * FEAT-006: Type Inference Review & Confirmation — the "Types" step/tab of
 * screens/07-ingest-confirmation.md. Renders a dense `<table>` on md+ viewports and a stacked list
 * below, sharing the same data and handlers, per the screen's Web/Mobile layout split.
 *
 * Both views are grouped by source dataset — never a flat cross-dataset list — per that screen's hard
 * requirement: a real incident produced 4 duplicate datasets whose 16 identical columns rendered as
 * an undifferentiated, unlabeled repeating list. Each group's sub-header states its `original_filename`
 * and `created_at`, which is what distinguishes two "Keep both" (FEAT-049) datasets sharing a name.
 */
export function TypeReviewStep({ columns, savingIds, errors, onChangeType }: TypeReviewStepProps) {
  // Client-only, in-flight override selection. Kept separate from the server-backed
  // `columns[].inferred_type` so the select AND the confidence badge update in the SAME interaction
  // as the dropdown change — FEAT-006's requirement ("no loading state, no fresh query"). The badge
  // reads the pending type's MEASURED fit rate from `type_fit_rates`, which is exactly why the
  // per-type rates ship with every column: an override's low fit rate is itself the warning.
  //
  // When a row has an error the server state (`col.inferred_type`) is the last-known-good value, so
  // the pending choice is dropped from display and the select + badge revert to it — matching
  // screens/07's Error state ("the user's in-progress selections are preserved, not lost"): the
  // user can re-pick, but the UI never shows a value the server rejected as if it stuck.
  const [pending, setPending] = useState<Record<string, InferredType>>({});

  function handleSelectChange(columnId: string, value: string) {
    setPending((prev) => ({ ...prev, [columnId]: value as InferredType }));
    onChangeType(columnId, value);
  }

  /** The type currently shown for a column: the in-flight override, else the server-backed value. */
  function activeType(col: ConfirmationColumn): InferredType {
    return errors[col.id] ? col.inferred_type : (pending[col.id] ?? col.inferred_type);
  }

  if (columns.length === 0) {
    return (
      <p className="py-8 text-center text-body-small text-text-secondary">
        No columns were profiled for this upload.
      </p>
    );
  }

  const groups = groupColumnsByDataset(columns);

  return (
    <>
      {/* Mobile: stacked list, grouped under a per-dataset heading. */}
      <div className="flex flex-col gap-4 md:hidden">
        {groups.map((group) => (
          <div key={group.datasetId}>
            <h4 className="mb-2 text-caption font-medium uppercase tracking-wide text-text-secondary">
              {datasetGroupLabel(group)}
            </h4>
            <ul className="flex flex-col gap-3">
              {group.columns.map((col) => {
                const type = activeType(col);
                const rate = typeFitRate(col, type);
                return (
                  <li key={col.id} className="rounded-md border border-border p-3">
                    <div className="mb-2 font-medium text-text-primary">{col.name}</div>
                    <ConfidenceBadge
                      tier={confidenceTier(rate)}
                      detail={`${Math.round(rate * 100)}% of values fit ${typeLabel(type)}`}
                    />
                    <label className="mt-2 flex flex-col gap-1">
                      <span className="text-body-small text-text-secondary">Column type</span>
                      <select
                        value={type}
                        disabled={savingIds.has(col.id)}
                        onChange={(e) => handleSelectChange(col.id, e.target.value)}
                        className="h-(--size-input-height) rounded-md border border-border bg-surface px-2 text-body text-text-primary disabled:opacity-50"
                      >
                        {TYPE_OPTIONS.map((opt) => (
                          <option key={opt.value} value={opt.value}>
                            {opt.label}
                          </option>
                        ))}
                      </select>
                    </label>
                    {errors[col.id] && <RowError message={errors[col.id]} />}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </div>

      {/* Web: dense table, with a sticky sub-header row per dataset. */}
      <div className="hidden md:block">
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
              <th className="px-2 py-2 font-medium">Column</th>
              <th className="px-2 py-2 font-medium">Inferred type</th>
              <th className="px-2 py-2 font-medium">Confidence</th>
              <th className="px-2 py-2 font-medium">Override</th>
            </tr>
          </thead>
          {groups.map((group) => (
            <tbody key={group.datasetId}>
              <tr className="sticky top-0 bg-surface-secondary">
                <th
                  colSpan={4}
                  scope="colgroup"
                  className="px-2 py-2 text-left text-body-small font-medium text-text-primary"
                >
                  {datasetGroupLabel(group)}
                </th>
              </tr>
              {group.columns.map((col) => {
                const type = activeType(col);
                const rate = typeFitRate(col, type);
                return (
                  <tr key={col.id} className="border-b border-border last:border-b-0">
                    <td className="px-2 py-2 font-medium text-text-primary">{col.name}</td>
                    <td className="px-2 py-2 text-body text-text-primary">{typeLabel(type)}</td>
                    <td className="px-2 py-2">
                      <ConfidenceBadge
                        tier={confidenceTier(rate)}
                        detail={`${Math.round(rate * 100)}% of values fit ${typeLabel(type)}`}
                      />
                    </td>
                    <td className="px-2 py-2">
                      <select
                        value={type}
                        disabled={savingIds.has(col.id)}
                        onChange={(e) => handleSelectChange(col.id, e.target.value)}
                        aria-label={`Override type for ${col.name}`}
                        className="h-(--size-input-height) rounded-md border border-border bg-surface px-2 text-body text-text-primary disabled:opacity-50"
                      >
                        {TYPE_OPTIONS.map((opt) => (
                          <option key={opt.value} value={opt.value}>
                            {opt.label}
                          </option>
                        ))}
                      </select>
                      {errors[col.id] && <RowError message={errors[col.id]} />}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          ))}
        </table>
      </div>
    </>
  );
}

/** Inline per-row error — the failed action's selections stay exactly as they were, per screens/07's
 * Error state ("user's in-progress selections are preserved, not lost"); only this row's control
 * shows the failure. */
export function RowError({ message }: { message: string }) {
  return (
    <p className="mt-1 flex items-center gap-1 text-body-small text-error" role="alert">
      <AlertCircle size={14} aria-hidden="true" />
      {message}
    </p>
  );
}
