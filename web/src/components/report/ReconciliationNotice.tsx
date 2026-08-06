import type { ReconciliationSummary } from "@/types/schema";

export interface ReconciliationNoticeProps {
  summary: ReconciliationSummary;
}

/**
 * The Reconciliation Notice (screens/09-report-view.md, screens.md Shared/Common Components): the
 * honest-status principle in data form. It appears ONLY when the filter pipeline crossed a
 * significant exclusion threshold — never shown-and-empty.
 *
 * The report shows rows_out (what the answer covers) against rows_in (what the question asked
 * about). When they differ meaningfully, the notice says so unprompted, with the plain-language
 * exclusion warnings the pipeline recorded.
 */
export function ReconciliationNotice({ summary }: ReconciliationNoticeProps) {
  const hasWarnings = summary.exclusion_warnings.length > 0;
  const rowsDropped = summary.rows_out < summary.rows_in;

  // Neither a dropped count nor a warning → nothing to disclose. The doc's "Loaded (no notice)"
  // state: the component is absent entirely, not rendered empty.
  if (!hasWarnings && !rowsDropped) {
    return null;
  }

  return (
    <div
      role="status"
      className="rounded-md border border-warning bg-warning/12 p-3 text-body-small text-text-primary"
    >
      <p className="font-medium">Reconciliation note</p>
      {rowsDropped && (
        <p className="mt-1 text-text-secondary">
          This answer covers {summary.rows_out.toLocaleString()} of{" "}
          {summary.rows_in.toLocaleString()} rows read.
        </p>
      )}
      {hasWarnings &&
        summary.exclusion_warnings.map((warning) => (
          <p key={warning} className="mt-1 text-text-secondary">
            {warning}
          </p>
        ))}
    </div>
  );
}