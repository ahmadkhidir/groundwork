import { ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { StatusPill } from "@/components/ui/StatusPill";
import { RowError } from "./TypeReviewStep";
import type { ConfirmationColumn } from "./types";

export interface PIIReviewStepProps {
  columns: ConfirmationColumn[];
  savingIds: Set<string>;
  errors: Record<string, string>;
  onToggleFlag: (columnId: string, nextFlag: boolean) => void;
}

/**
 * FEAT-007: PII Detection & Flagging at Ingest — the "Sensitive columns" step/tab of
 * screens/07-ingest-confirmation.md.
 *
 * Per FEAT-007's acceptance criteria, a flagged column already defaults to name-and-type-only
 * exposure — this step exists so the user can review that automatic decision and override it
 * without needing to understand the underlying detection method (name heuristics, value-shape
 * regexes) at all. Overriding a flag to "not sensitive" is an explicit, logged decision — see
 * ColumnRepository.UpdatePII's doc comment on IMPLEMENTATION.md's open-items table for the one gap
 * this wave could not close: schema.md has no field distinguishing that explicit-override history
 * from a column that was simply never flagged.
 */
export function PIIReviewStep({ columns, savingIds, errors, onToggleFlag }: PIIReviewStepProps) {
  if (columns.length === 0) {
    return (
      <p className="py-8 text-center text-body-small text-text-secondary">
        No columns were profiled for this upload.
      </p>
    );
  }

  const flaggedCount = columns.filter((c) => c.pii_flag).length;

  return (
    <>
      <p className="mb-3 flex items-center gap-2 text-body-small text-text-secondary">
        <ShieldAlert size={16} className="text-warning" aria-hidden="true" />
        {flaggedCount === 0
          ? "No columns were automatically flagged as sensitive."
          : `${flaggedCount} column${flaggedCount === 1 ? "" : "s"} flagged — values stay out of every payload sent to the model by default.`}
      </p>

      {/* Mobile: stacked list. */}
      <ul className="flex flex-col gap-3 md:hidden">
        {columns.map((col) => (
          <li key={col.id} className="rounded-md border border-border p-3">
            <div className="mb-1 text-caption uppercase tracking-wide text-text-secondary">
              {col.dataset_filename}
            </div>
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="font-medium text-text-primary">{col.name}</span>
              {col.pii_flag ? (
                <StatusPill variant="warning">{col.pii_pattern || "flagged"}</StatusPill>
              ) : (
                <StatusPill variant="neutral">not sensitive</StatusPill>
              )}
            </div>
            <Button
              variant="outline"
              loading={savingIds.has(col.id)}
              onClick={() => onToggleFlag(col.id, !col.pii_flag)}
            >
              {col.pii_flag ? "Mark not sensitive" : "Flag as sensitive"}
            </Button>
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
              <th className="px-2 py-2 font-medium">Status</th>
              <th className="px-2 py-2 font-medium">Action</th>
            </tr>
          </thead>
          <tbody>
            {columns.map((col) => (
              <tr key={col.id} className="border-b border-border last:border-b-0">
                <td className="px-2 py-2 text-body-small text-text-secondary">{col.dataset_filename}</td>
                <td className="px-2 py-2 font-medium text-text-primary">{col.name}</td>
                <td className="px-2 py-2">
                  {col.pii_flag ? (
                    <StatusPill variant="warning">{col.pii_pattern || "flagged"}</StatusPill>
                  ) : (
                    <StatusPill variant="neutral">not sensitive</StatusPill>
                  )}
                </td>
                <td className="px-2 py-2">
                  <Button
                    variant="outline"
                    loading={savingIds.has(col.id)}
                    onClick={() => onToggleFlag(col.id, !col.pii_flag)}
                  >
                    {col.pii_flag ? "Mark not sensitive" : "Flag as sensitive"}
                  </Button>
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
