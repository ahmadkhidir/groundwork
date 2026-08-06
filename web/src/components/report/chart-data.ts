/**
 * Pure helpers the first-party chart renderer (FEAT-017) shares between its SVG renderer and its
 * accessible data-table fallback.
 *
 * Keeping the column-resolution and number-coercion logic here — separate from JSX — makes the
 * renderer's malformed-data behavior unit-testable without mounting SVG, and keeps a single
 * definition of "what counts as numeric" for both the chart and the table.
 */

import type { ResultValue } from "@/components/run/step-payloads";

/** Index of `column` in `columns`, or -1 when absent or the name is empty. */
export function resolveColumnIndex(columns: string[], column: string): number {
  if (column.length === 0) return -1;
  const index = columns.indexOf(column);
  return index;
}

/**
 * Coerces a result cell to a number, or null when it is not numeric.
 *
 * JSON scalars from the execute step are `string | number | boolean | null`. Booleans are not
 * numeric; a numeric-looking string ("42") is accepted because the execute step can stream numeric
 * values back as strings depending on the engine's typing.
 */
export function toNumber(value: ResultValue | undefined): number | null {
  if (typeof value === "number") return Number.isFinite(value) ? value : null;
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (trimmed.length === 0) return null;
    const parsed = Number(trimmed);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

/** A row's value rendered as a display string; null values read as the visible "—". */
export function displayValue(value: ResultValue | undefined): string {
  if (value === null || value === undefined) return "—";
  return String(value);
}

/**
 * Whether a chart type is one the renderer actually draws. Anything else (a future chart type not
 * yet implemented) is the 09-report-view.md "future-proofing" edge case: fall back to the table.
 */
export function isSupportedChartType(
  chartType: string,
): chartType is "bar" | "line" | "pie" | "scatter" {
  return chartType === "bar" || chartType === "line" || chartType === "pie" || chartType === "scatter";
}