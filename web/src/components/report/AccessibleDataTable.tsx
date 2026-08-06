import type { ExecuteStepOutput } from "@/components/run/step-payloads";
import { displayValue } from "./chart-data";

export interface AccessibleDataTableProps {
  result: ExecuteStepOutput;
  /** Optional caption, e.g. "Result data for 'Sales by region'". */
  caption?: string;
}

/**
 * The accessible data table (screens/09-report-view.md): the screen-reader/text equivalent of a
 * rendered chart. Used both as the primary view for table-type specs and as the fallback when a
 * chart type isn't (yet) supported by the renderer.
 *
 * Distinct from the Run Viewer's live ResultTable in its role rather than its markup: this table is
 * the chart-equivalent surface on the Report, so it carries a `<caption>` naming the chart it backs.
 */
export function AccessibleDataTable({ result, caption }: AccessibleDataTableProps) {
  return (
    <div className="overflow-x-auto rounded-md border border-border">
      <table className="w-full border-collapse text-body-small">
        {caption && (
          <caption className="sr-only">{caption}</caption>
        )}
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
                  {displayValue(cell)}
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