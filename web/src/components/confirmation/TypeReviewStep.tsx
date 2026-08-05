import { AlertCircle } from "lucide-react";
import { InferredType } from "@/types/schema";
import { ConfidenceBadge } from "./ConfidenceBadge";
import { confidenceTier, type ConfirmationColumn } from "./types";

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
 */
export function TypeReviewStep({ columns, savingIds, errors, onChangeType }: TypeReviewStepProps) {
  if (columns.length === 0) {
    return (
      <p className="py-8 text-center text-body-small text-text-secondary">
        No columns were profiled for this upload.
      </p>
    );
  }

  return (
    <>
      {/* Mobile: stacked list, one card per column. */}
      <ul className="flex flex-col gap-3 md:hidden">
        {columns.map((col) => (
          <li key={col.id} className="rounded-md border border-border p-3">
            <div className="mb-1 text-caption uppercase tracking-wide text-text-secondary">
              {col.dataset_filename}
            </div>
            <div className="mb-2 font-medium text-text-primary">{col.name}</div>
            <ConfidenceBadge
              tier={confidenceTier(col.type_confidence)}
              detail={`${Math.round(col.type_confidence * 100)}% confident — ${typeLabel(col.inferred_type)}`}
            />
            <label className="mt-2 flex flex-col gap-1">
              <span className="text-body-small text-text-secondary">Column type</span>
              <select
                value={col.inferred_type}
                disabled={savingIds.has(col.id)}
                onChange={(e) => onChangeType(col.id, e.target.value)}
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
        ))}
      </ul>

      {/* Web: dense table. */}
      <div className="hidden md:block">
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
              <th className="px-2 py-2 font-medium">File</th>
              <th className="px-2 py-2 font-medium">Column</th>
              <th className="px-2 py-2 font-medium">Inferred type</th>
              <th className="px-2 py-2 font-medium">Confidence</th>
              <th className="px-2 py-2 font-medium">Override</th>
            </tr>
          </thead>
          <tbody>
            {columns.map((col) => (
              <tr key={col.id} className="border-b border-border last:border-b-0">
                <td className="px-2 py-2 text-body-small text-text-secondary">{col.dataset_filename}</td>
                <td className="px-2 py-2 font-medium text-text-primary">{col.name}</td>
                <td className="px-2 py-2 text-body text-text-primary">{typeLabel(col.inferred_type)}</td>
                <td className="px-2 py-2">
                  <ConfidenceBadge
                    tier={confidenceTier(col.type_confidence)}
                    detail={`${Math.round(col.type_confidence * 100)}%`}
                  />
                </td>
                <td className="px-2 py-2">
                  <select
                    value={col.inferred_type}
                    disabled={savingIds.has(col.id)}
                    onChange={(e) => onChangeType(col.id, e.target.value)}
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
            ))}
          </tbody>
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
