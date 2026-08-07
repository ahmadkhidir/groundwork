/**
 * Per-step payload types for the run DAG (FEAT-013, Phase 2).
 *
 * `Step.input` and `Step.output` are JSON-encoded STRINGS (schema.ts's convention on `Step`): the
 * shape genuinely varies by `step_name`, so these types accompany the Run Viewer and parse at the
 * boundary rather than widening the shared schema. They mirror the Go shapes in
 * server/internal/domain/run.go — keep them in lockstep.
 *
 * The steps exist here: plan (with FEAT-033's clarification fields), generate_sql, static_check,
 * execute, shadow_check (FEAT-031), and adversarial_review (FEAT-032). reconcile, chart_spec,
 * narrate, and assemble have no payload shape on the client yet.
 */

/** Parsed output of the "plan" step: whether the question is answerable, and how. */
export interface PlanStepOutput {
  answerable: boolean;
  /** Plain-language explanation, populated when `answerable` is false. */
  reason?: string;
  /** Short approach for generate_sql, populated when `answerable` is true. */
  approach?: string;
  /**
   * FEAT-033 (Honest Uncertainty): true when the plan judged the question AMBIGUOUS and paused the
   * run for a specific clarifying question instead of proceeding on a guess.
   */
  needs_clarification?: boolean;
  /** The SPECIFIC clarifying question, e.g. "Do you mean revenue by total value or by order count?" */
  clarification_question?: string;
  /** Discrete resolution choices when the ambiguity has an enumerable set, e.g. ["By revenue", "By count"]. */
  clarification_options?: string[];
  /** Stable short key naming the ambiguity, used to not re-ask functionally identical follow-ups. */
  ambiguity_key?: string;
  /** The user's answer, written back into the plan output when the clarify endpoint resumes the run. */
  clarification_answer?: string;
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
 * One FEAT-031 shadow-check result: an aggregate total cross-checked against an independently
 * computed value. `matched: false` is a real disagreement surfaced to the user — never silently
 * discarded.
 */
export interface ShadowCheck {
  /** Result column (alias) whose values were cross-checked. */
  column: string;
  /** Aggregate total computed from the executed result set. */
  primary_value: number;
  /** Same aggregate recomputed directly over the source table (no join). */
  shadow_value: number;
  /** False when the two computations disagree beyond rounding tolerance. */
  matched: boolean;
  /** Plain-language explanation of the check's outcome. */
  message: string;
}

/** Parsed output of the "shadow_check" step (FEAT-031). */
export interface ShadowCheckStepOutput {
  checks: ShadowCheck[];
  /** Explains why no cross-check was computed. Empty when `checks` is non-empty. */
  skipped_reason?: string;
}

/**
 * Parsed output of the "adversarial_review" step (FEAT-032): the verdict of a separate model pass
 * that saw ONLY the question and the SQL — never the results. A blocking mismatch fails the step
 * (its `output` then holds the verdict as the failure reason); a warning mismatch completes the
 * step with `matches: false, severity: "warning"` so the viewer can flag it.
 */
export interface AdversarialReviewStepOutput {
  matches: boolean;
  verdict: string;
  severity?: "ok" | "warning" | "blocking";
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

/** Parses a shadow_check step's output, or null when it is absent or malformed (FEAT-031). */
export function parseShadowCheckStep(output: string | undefined): ShadowCheckStepOutput | null {
  const parsed = parseStepOutput("shadow_check", output);
  if (parsed === null || !Array.isArray((parsed as ShadowCheckStepOutput).checks)) {
    return null;
  }
  return parsed as ShadowCheckStepOutput;
}

/** Parses an adversarial_review step's output, or null when it is absent or malformed (FEAT-032). */
export function parseAdversarialReviewStep(
  output: string | undefined,
): AdversarialReviewStepOutput | null {
  const parsed = parseStepOutput("adversarial_review", output);
  if (parsed === null || typeof (parsed as AdversarialReviewStepOutput).matches !== "boolean") {
    return null;
  }
  return parsed as AdversarialReviewStepOutput;
}
