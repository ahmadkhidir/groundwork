import { ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { StatusPill } from "@/components/ui/StatusPill";
import { isPresent } from "@/types/schema";
import { RowError } from "./TypeReviewStep";
import type { ConfirmationColumn } from "./types";

/**
 * The PII state of one column, as three distinct readings rather than two.
 *
 * FEAT-007 requires that "a column that was reviewed-and-cleared is distinguishable later from one
 * that was simply never flagged" — so an unreviewed unflagged column and a deliberately cleared one
 * must not render identically, even though both carry `pii_flag: false`.
 *
 * Every state carries a text label, never colour alone (branding.md's colour-independence rule).
 */
function PIIStatus({ column }: { column: ConfirmationColumn }) {
  const reviewed = isPresent(column.pii_reviewed_at);
  const reviewedLabel = reviewed
    ? `You reviewed this on ${new Date(column.pii_reviewed_at as string).toLocaleDateString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
      })}`
    : null;

  return (
    <div className="flex flex-col items-start gap-1">
      {column.pii_flag ? (
        <StatusPill variant="warning">{column.pii_pattern || "flagged"}</StatusPill>
      ) : reviewed ? (
        <StatusPill variant="neutral">cleared by you</StatusPill>
      ) : (
        <StatusPill variant="neutral">not sensitive</StatusPill>
      )}
      {reviewedLabel && (
        <span className="text-caption text-text-secondary">{reviewedLabel}</span>
      )}
    </div>
  );
}

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
 * regexes) at all.
 *
 * Overriding a flag is an explicit, logged decision: the server stamps `Column.pii_reviewed_at` and
 * writes a `pii_override` audit entry in the same request, and PIIStatus below renders that reviewed
 * state so a cleared column reads differently from one that was never flagged.
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
            <div className="mb-2 flex items-start justify-between gap-2">
              <span className="font-medium text-text-primary">{col.name}</span>
              <PIIStatus column={col} />
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
                  <PIIStatus column={col} />
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
