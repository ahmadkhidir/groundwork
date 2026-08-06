import type { ChartSpec } from "@/types/schema";
import type { ExecuteStepOutput, ResultValue } from "@/components/run/step-payloads";
import {
  displayValue,
  isSupportedChartType,
  resolveColumnIndex,
  toNumber,
} from "./chart-data";

/**
 * Fixed SVG canvas for every chart type. A viewBox, not pixel sizes, so the renderer scales to any
 * container (web columns and mobile full-width alike) without a second layout pass.
 */
const WIDTH = 640;
const HEIGHT = 320;
const PAD = { top: 24, right: 16, bottom: 40, left: 48 };

const PLOT_WIDTH = WIDTH - PAD.left - PAD.right;
const PLOT_HEIGHT = HEIGHT - PAD.top - PAD.bottom;

/**
 * Categorical palette in FIXED order, one class per token (branding.md §Chart Colors):
 * the same metric renders in the same color across reports. Never inline a raw hex here.
 */
const CAT_FILLS = [
  "fill-chart-cat-1",
  "fill-chart-cat-2",
  "fill-chart-cat-3",
  "fill-chart-cat-4",
  "fill-chart-cat-5",
  "fill-chart-cat-6",
  "fill-chart-cat-7",
  "fill-chart-cat-8",
] as const;

const CAT_STROKES = [
  "stroke-chart-cat-1",
  "stroke-chart-cat-2",
  "stroke-chart-cat-3",
  "stroke-chart-cat-4",
  "stroke-chart-cat-5",
  "stroke-chart-cat-6",
  "stroke-chart-cat-7",
  "stroke-chart-cat-8",
] as const;

/** A single charted category/value/series point. */
interface Point {
  label: string;
  value: number | null;
  series: string;
}

export interface ChartRendererProps {
  spec: ChartSpec;
  /** The execute step's result data the chart draws from. Null when the step never completed. */
  result: ExecuteStepOutput | null;
}

/**
 * First-party chart renderer (FEAT-017, screens/09-report-view.md "Chart").
 *
 * Charts are specs, never images: the report's `chart_spec` plus the run's result data produce SVG
 * here in the web client — no charting library, no raster asset. Every color comes from the chart
 * tokens in globals.css through the class names above.
 *
 * Malformed or unsupported input never crashes the report:
 *  - A chart type the renderer does not (yet) draw, or a spec column missing from the result set,
 *    returns the unsupported note; the caller renders the accessible data table fallback (the
 *    09-report-view.md "future-proofing" edge case).
 *  - Non-numeric cells in a numeric axis are skipped; an all-empty result renders an explicit
 *    empty state rather than a misleading graphic.
 */
export function ChartRenderer({ spec, result }: ChartRendererProps) {
  if (!result || !isSupportedChartType(spec.chart_type) || !hasSpecColumns(result, spec)) {
    return (
      <p className="text-body-small text-text-secondary" data-testid="chart-unsupported-note">
        {!result
          ? "No result data to chart."
          : "This chart type isn't supported yet — showing the data table instead."}
      </p>
    );
  }

  const points = toPoints(result, spec);
  const chartType = spec.chart_type;

  const graphic =
    chartType === "bar" ? (
      <BarChart points={points} />
    ) : chartType === "line" ? (
      <LineChart points={points} />
    ) : chartType === "pie" ? (
      <PieChart points={points} />
    ) : (
      <ScatterChart points={points} />
    );

  return (
    <div data-testid="chart-renderer">
      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label={spec.title || "Chart"}
        className="h-auto w-full"
      >
        <title>{spec.title || "Chart"}</title>
        {graphic}
      </svg>
    </div>
  );
}

/** True when every non-empty spec column resolves in the result columns. */
function hasSpecColumns(result: ExecuteStepOutput, spec: ChartSpec): boolean {
  const names = [spec.x_axis_column, spec.y_axis_column, spec.series_column ?? ""];
  return names.every((name) => name.length === 0 || result.columns.includes(name));
}

/** Flattens result rows into chart points with resolved column indices. */
function toPoints(result: ExecuteStepOutput, spec: ChartSpec): Point[] {
  const xIndex = resolveColumnIndex(result.columns, spec.x_axis_column);
  const yIndex = resolveColumnIndex(result.columns, spec.y_axis_column);
  const seriesIndex = spec.series_column
    ? resolveColumnIndex(result.columns, spec.series_column)
    : -1;
  return result.rows.map((row) => ({
    label: displayValue(xIndex >= 0 ? row[xIndex] : undefined),
    value: yIndex >= 0 ? toNumber(row[yIndex] as ResultValue | undefined) : null,
    series: seriesIndex >= 0 ? displayValue(row[seriesIndex]) : "",
  }));
}

/** Unique ordered values of a field across points. */
function uniqueValues(points: Point[], field: "label" | "series"): string[] {
  const seen: string[] = [];
  for (const point of points) {
    const value = point[field];
    if (!seen.includes(value)) seen.push(value);
  }
  return seen;
}

function fillFor(series: string, seriesNames: string[]): string {
  const index = seriesNames.indexOf(series);
  return CAT_FILLS[Math.max(0, index) % CAT_FILLS.length];
}

function strokeFor(series: string, seriesNames: string[]): string {
  const index = seriesNames.indexOf(series);
  return CAT_STROKES[Math.max(0, index) % CAT_STROKES.length];
}

/** Formats an axis tick: large numbers compact, small numbers with at most one decimal. */
function formatTick(value: number): string {
  if (Math.abs(value) >= 1_000) return `${Math.round(value / 1_000)}k`;
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

/** A horizontal baseline with four labeled gridlines, sharing the caller's linear y-scale. */
function AxisGrid({ scaleMax }: { scaleMax: number }) {
  const steps = 4;
  const lines = Array.from({ length: steps + 1 }, (_, i) => (scaleMax * i) / steps);
  return (
    <g aria-hidden="true">
      {lines.map((value) => {
        const y = PAD.top + PLOT_HEIGHT - (value / scaleMax) * PLOT_HEIGHT;
        return (
          <g key={value}>
            <line
              x1={PAD.left}
              x2={WIDTH - PAD.right}
              y1={y}
              y2={y}
              className="stroke-border"
              strokeWidth={1}
            />
            <text
              x={PAD.left - 6}
              y={y + 4}
              textAnchor="end"
              className="text-caption fill-text-secondary"
            >
              {formatTick(value)}
            </text>
          </g>
        );
      })}
    </g>
  );
}

/* --------------------------------------------------------------------------------------------
 * Bar
 * ------------------------------------------------------------------------------------------ */

function BarChart({ points }: { points: Point[] }) {
  const seriesNames = uniqueValues(points, "series");
  const categories = uniqueValues(points, "label");
  const scaleMax = Math.max(1, ...points.map((point) => point.value ?? 0));

  const groupWidth = PLOT_WIDTH / Math.max(1, categories.length);
  const barWidth = Math.max(2, groupWidth / Math.max(1, seriesNames.length) - 4);

  return (
    <g>
      <AxisGrid scaleMax={scaleMax} />
      {categories.map((category, catIndex) => {
        const x = PAD.left + catIndex * groupWidth;
        return seriesNames.map((series, seriesIndex) => {
          const point = points.find(
            (p) => p.label === category && p.series === series,
          );
          const value = point?.value ?? null;
          const barHeight = value === null ? 0 : (value / scaleMax) * PLOT_HEIGHT;
          return (
            <g key={`${category}-${series}`}>
              <rect
                x={x + seriesIndex * barWidth + 2}
                y={PAD.top + PLOT_HEIGHT - barHeight}
                width={barWidth}
                height={Math.max(barHeight, 0)}
                className={fillFor(series, seriesNames)}
              />
              {categories.length <= 10 && seriesIndex === 0 && (
                <text
                  x={x + groupWidth / 2}
                  y={PAD.top + PLOT_HEIGHT + 16}
                  textAnchor="middle"
                  className="text-caption fill-text-secondary"
                >
                  {category}
                </text>
              )}
            </g>
          );
        });
      })}
    </g>
  );
}

/* --------------------------------------------------------------------------------------------
 * Line
 * ------------------------------------------------------------------------------------------ */

function LineChart({ points }: { points: Point[] }) {
  const seriesNames = uniqueValues(points, "series");
  const scaleMax = Math.max(1, ...points.map((point) => point.value ?? 0));

  const xValues = points.map((point) => toNumber(point.label));
  const maxX = Math.max(0, ...xValues.filter((x): x is number => x !== null));
  const useNumericX = maxX > 0;

  return (
    <g>
      <AxisGrid scaleMax={scaleMax} />
      {seriesNames.map((series) => {
        const seriesPoints = points.filter((point) => point.series === series);
        const pts = seriesPoints
          .map((point, i) => {
            const xNum = toNumber(point.label);
            const x =
              useNumericX && xNum !== null
                ? PAD.left + (xNum / maxX) * PLOT_WIDTH
                : PAD.left + (i / Math.max(1, seriesPoints.length - 1)) * PLOT_WIDTH;
            const y =
              PAD.top + PLOT_HEIGHT - ((point.value ?? 0) / scaleMax) * PLOT_HEIGHT;
            return `${x},${y}`;
          })
          .join(" ");
        return (
          <polyline
            key={series}
            points={pts}
            fill="none"
            strokeWidth={2}
            className={strokeFor(series, seriesNames)}
          />
        );
      })}
    </g>
  );
}

/* --------------------------------------------------------------------------------------------
 * Pie
 * ------------------------------------------------------------------------------------------ */

function polar(cx: number, cy: number, r: number, angle: number): { x: number; y: number } {
  return { x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) };
}

function piePath(
  cx: number,
  cy: number,
  r: number,
  startAngle: number,
  endAngle: number,
): string {
  const start = polar(cx, cy, r, startAngle);
  const end = polar(cx, cy, r, endAngle);
  const largeArc = endAngle - startAngle > Math.PI ? 1 : 0;
  return `M ${cx} ${cy} L ${start.x} ${start.y} A ${r} ${r} 0 ${largeArc} 1 ${end.x} ${end.y} Z`;
}

function PieChart({ points }: { points: Point[] }) {
  const totals = new Map<string, number>();
  for (const point of points) {
    if (point.value !== null) {
      totals.set(point.label, (totals.get(point.label) ?? 0) + point.value);
    }
  }
  const entries = Array.from(totals.entries());
  const sum = entries.reduce((acc, [, value]) => acc + value, 0);
  if (sum === 0) {
    return <p className="text-body-small text-text-secondary">No numeric values to chart.</p>;
  }

  const cx = WIDTH / 2;
  const cy = HEIGHT / 2;
  const r = Math.min(PLOT_WIDTH, PLOT_HEIGHT) / 2 - 8;

  let cursor = -Math.PI / 2;
  return (
    <g aria-label="Pie chart">
      {entries.map(([label, value]) => {
        const sweep = (value / sum) * Math.PI * 2;
        const start = cursor;
        const end = cursor + sweep;
        cursor = end;
        return (
          <path
            key={label}
            d={piePath(cx, cy, r, start, end)}
            className={fillFor(label, entries.map(([name]) => name))}
          />
        );
      })}
    </g>
  );
}

/* --------------------------------------------------------------------------------------------
 * Scatter
 * ------------------------------------------------------------------------------------------ */

function ScatterChart({ points }: { points: Point[] }) {
  const seriesNames = uniqueValues(points, "series");
  const scaleMax = Math.max(1, ...points.map((point) => point.value ?? 0));
  const xValues = points.map((point) => toNumber(point.label));
  const maxX = Math.max(0, ...xValues.filter((x): x is number => x !== null));

  return (
    <g>
      <AxisGrid scaleMax={scaleMax} />
      {points.map((point, index) => {
        const xNum = toNumber(point.label);
        if (xNum === null || maxX === 0) return null;
        const x = PAD.left + (xNum / maxX) * PLOT_WIDTH;
        const y = PAD.top + PLOT_HEIGHT - ((point.value ?? 0) / scaleMax) * PLOT_HEIGHT;
        return (
          <circle
            key={index}
            cx={x}
            cy={y}
            r={4}
            className={fillFor(point.series, seriesNames)}
          />
        );
      })}
    </g>
  );
}