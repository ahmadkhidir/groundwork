import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StepFeed } from "./StepFeed";
import type { Step, StepName } from "@/types/schema";

function step(
  stepName: StepName,
  overrides: Partial<Omit<Step, "step_name" | "idempotency_key">> = {},
): Step {
  return {
    id: `step-${stepName}`,
    run_id: "run-1",
    step_name: stepName,
    status: "complete",
    idempotency_key: `run-1:${stepName}`,
    started_at: "2026-01-01T00:00:00Z",
    completed_at: "2026-01-01T00:00:02Z",
    ...overrides,
  };
}

describe("StepFeed", () => {
  it("renders a completed static_check step's warnings (FEAT-028 AC2)", () => {
    const warnings = [
      {
        category: "null_dropping_filter",
        message: "Column \"email\" is 30% null and is used in a filter without a null guard.",
      },
      {
        category: "aggregate_over_join_without_dedup",
        message: "This query aggregates over a JOIN without dedup.",
      },
    ];
    render(
      <StepFeed
        steps={[
          step("plan"),
          step("generate_sql"),
          step("static_check", {
            output: JSON.stringify({ warnings }),
          }),
        ]}
      />,
    );

    expect(screen.getByText("Static check")).toBeTruthy();
    expect(screen.getByText(warnings[0].message)).toBeTruthy();
    expect(screen.getByText(warnings[1].message)).toBeTruthy();
  });

  it("renders no warnings for a clean static_check (empty list)", () => {
    const { container } = render(
      <StepFeed
        steps={[step("static_check", { output: JSON.stringify({ warnings: [] }) })]}
      />,
    );

    expect(screen.getByText("Static check")).toBeTruthy();
    // The feed's own <ol> exists; no inner warning <ul> should.
    expect(container.querySelector("ul")).toBeNull();
  });

  it("renders no warnings when the static_check output is absent or malformed", () => {
    const { container } = render(
      <StepFeed steps={[step("static_check", { output: "definitely not json" })]} />,
    );

    expect(screen.getByText("Static check")).toBeTruthy();
    expect(container.textContent).not.toContain("null");
  });

  it("renders a halted step with the halted label", () => {
    render(
      <StepFeed
        steps={[step("execute", { status: "halted" })]}
      />,
    );

    expect(screen.getByText("Execute")).toBeTruthy();
    expect(screen.getByText("Halted")).toBeTruthy();
  });

  it("renders a failed execute step's reason stripped of the Go wrapper noise (FEAT-014)", () => {
    const raw =
      "executing query: engine: query: Binder Error: GROUP BY clause cannot contain window functions!\n" +
      "LINE 1: ...ulation AS (SELECT position, CASE WHEN defensive_actions < PERCENTILE_CONT(0.33) WITHIN GROUP (ORDER BY defensive_actions";
    render(<StepFeed steps={[step("execute", { status: "failed", output: raw })]} />);

    // The reason reads as the engine's message, not an internal error chain.
    expect(
      screen.getByText("Binder Error: GROUP BY clause cannot contain window functions!"),
    ).toBeTruthy();
    expect(screen.queryByText(/executing query:/)).toBeNull();
    expect(screen.queryByText(/engine: query:/)).toBeNull();
    // DuckDB's long LINE echo is behind a disclosure — present but not dumped inline.
    expect(screen.getByText("Engine detail")).toBeTruthy();
    expect(screen.getByText(/LINE 1:/)).toBeTruthy();
  });

  it("passes a plain failed-step message through unchanged", () => {
    render(
      <StepFeed steps={[step("generate_sql", { status: "failed", output: "The model returned no SQL." })]} />,
    );

    expect(screen.getByText("The model returned no SQL.")).toBeTruthy();
    expect(screen.queryByText("Engine detail")).toBeNull();
  });
});
