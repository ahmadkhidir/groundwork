import type { ExecuteStepOutput } from "./step-payloads";

export interface ResultTableProps {
  result: ExecuteStepOutput;
}

/** Renders a cell value; null renders as the visible "—" rather than disappearing. */
function formatCell(value: unknown): string {
  if (value === null || value === undefined) return "—";
  return String(value);
}

/**
 * The executed result set (FEAT-014's facts) as a table: `execute` step output columns/rows.
 *
 * The run viewer's live results surface for Phase 2. No pagination or formatting knobs yet — this is
 * the facts display, and reports/charts (Phase 3+) own the richer renderings.
 */
export function ResultTable({ result }: ResultTableProps) {
  if (result.columns.length === 0) {
    return <p className="text-body-small text-text-secondary">No result columns.</p>;
  }

  return (
    <div className="overflow-x-auto rounded-md border border-border">
      <table className="w-full border-collapse text-body-small">
        <thead>
          <tr className="bg-surface-secondary text-left">
            {result.columns.map((column) => (
              <th
                key={column}
                scope="col"
                className="px-3 py-2 font-medium whitespace-nowrap text-text-primary"
              >
                {column}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {result.rows.map((row, rowIndex) => (
            <tr key={rowIndex} className="border-t border-border">
              {row.map((cell, cellIndex) => (
                <td
                  key={`${rowIndex}-${cellIndex}`}
                  className="px-3 py-2 whitespace-nowrap text-text-secondary"
                >
                  {formatCell(cell)}
                </td>
              ))}
            </tr>
          ))}
          {result.rows.length === 0 && (
            <tr className="border-t border-border">
              <td colSpan={result.columns.length} className="px-3 py-2 text-text-secondary">
                No rows returned.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
