/**
 * Per-step payload types for the run DAG (FEAT-013, Phase 2).
 *
 * `Step.input` and `Step.output` are JSON-encoded STRINGS (schema.ts's convention on `Step`): the
 * shape genuinely varies by `step_name`, so these types accompany the Run Viewer and parse at the
 * boundary rather than widening the shared schema. They mirror the Go shapes in
 * server/internal/domain/run.go — keep them in lockstep.
 *
 * The Phase 2 steps plus FEAT-028's static_check exist here: plan, generate_sql, static_check,
 * execute. reconcile, shadow_check, adversarial_review, chart_spec, narrate, and assemble have no
 * payload shape on the client yet.
 */

/** Parsed output of the "plan" step: whether the question is answerable, and how. */
export interface PlanStepOutput {
  answerable: boolean;
  /** Plain-language explanation, populated when `answerable` is false. */
  reason?: string;
  /** Short approach for generate_sql, populated when `answerable` is true. */
  approach?: string;
}

/** Parsed output of the "generate_sql" step: the SQL text that execute will run. */
export interface GenerateSQLStepOutput {
  sql: string;
}

/** A single result value: JSON scalar (string, number, boolean, or null). */
export type ResultValue = string | number | boolean | null;

/**
 * One FEAT-028 static-check finding: a risky pattern in the generated SQL that the user should
 * know about. Warnings are advisory — they never block a run.
 */
export interface StaticCheckWarning {
  /** Stable category id, e.g. "aggregate_over_join_without_dedup" or "null_dropping_filter". */
  category: string;
  /** Plain-language explanation of what the pattern risks. */
  message: string;
}

/** Parsed output of the "static_check" step: a deterministic, model-free analysis of the SQL. */
export interface StaticCheckStepOutput {
  warnings: StaticCheckWarning[];
}

/** Parsed output of the "execute" step: the facts returned from running the generated SQL. */
export interface ExecuteStepOutput {
  columns: string[];
  /** One array per row, in `columns` order. */
  rows: ResultValue[][];
  row_count: number;
}

/**
 * Parses a step's JSON-encoded output as the shape for its step name.
 *
 * Returns null when the output is absent, empty, or not the expected shape — the viewer then treats
 * the step as having no readable payload rather than crashing on malformed persisted data.
 */
export function parseStepOutput(stepName: string, output: string | undefined): unknown {
  if (!output) return null;
  try {
    const parsed: unknown = JSON.parse(output);
    if (typeof parsed !== "object" || parsed === null) return null;
    return parsed;
  } catch {
    return null;
  }
}

/** Parses a plan step's output, or null when it is absent or malformed. */
export function parsePlanStep(output: string | undefined): PlanStepOutput | null {
  const parsed = parseStepOutput("plan", output);
  if (parsed === null || typeof (parsed as PlanStepOutput).answerable !== "boolean") {
    return null;
  }
  return parsed as PlanStepOutput;
}

/** Parses a generate_sql step's output, or null when it is absent or malformed. */
export function parseGenerateSQLStep(output: string | undefined): GenerateSQLStepOutput | null {
  const parsed = parseStepOutput("generate_sql", output);
  if (parsed === null || typeof (parsed as GenerateSQLStepOutput).sql !== "string") {
    return null;
  }
  return parsed as GenerateSQLStepOutput;
}

/** Parses a static_check step's output, or null when it is absent or malformed. */
export function parseStaticCheckStep(output: string | undefined): StaticCheckStepOutput | null {
  const parsed = parseStepOutput("static_check", output);
  if (parsed === null || !Array.isArray((parsed as StaticCheckStepOutput).warnings)) {
    return null;
  }
  return parsed as StaticCheckStepOutput;
}

/** Parses an execute step's output, or null when it is absent or malformed. */
export function parseExecuteStep(output: string | undefined): ExecuteStepOutput | null {
  const parsed = parseStepOutput("execute", output);
  if (
    parsed === null ||
    !Array.isArray((parsed as ExecuteStepOutput).columns) ||
    !Array.isArray((parsed as ExecuteStepOutput).rows)
  ) {
    return null;
  }
  return parsed as ExecuteStepOutput;
}
