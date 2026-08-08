import { describe, expect, it } from "vitest";
import { friendlyStepError, splitStepError } from "./step-payloads";

describe("friendlyStepError", () => {
  it("strips the Go wrapper chain from a DuckDB execute failure, leaving the engine's reason", () => {
    const raw =
      "executing query: engine: query: Binder Error: GROUP BY clause cannot contain window functions!\n" +
      "LINE 1: ...ulation AS (SELECT position, CASE WHEN defensive_actions < PERCENTILE_CONT(0.33)";
    expect(friendlyStepError(raw)).toBe(
      "Binder Error: GROUP BY clause cannot contain window functions!\n" +
        "LINE 1: ...ulation AS (SELECT position, CASE WHEN defensive_actions < PERCENTILE_CONT(0.33)",
    );
  });

  it("strips nested wrappers regardless of order", () => {
    expect(friendlyStepError("engine: query: executing query: nope")).toBe("nope");
  });

  it("passes plain text through unchanged", () => {
    const plain = "The model returned no SQL.";
    expect(friendlyStepError(plain)).toBe(plain);
  });

  it("passes the row-ceiling message through unchanged (no wrapper to strip)", () => {
    const ceiling =
      "This query returned 12000 rows, more than the 10000-row limit per answer. Refine the question to return fewer rows \u2014 for example by adding a filter or using an aggregate \u2014 and try again.";
    expect(friendlyStepError(ceiling)).toBe(ceiling);
  });
});

describe("splitStepError", () => {
  it("splits a wrapped DuckDB error into a first-line summary and the LINE echo as detail", () => {
    const { summary, detail } = splitStepError(
      "executing query: engine: query: Binder Error: GROUP BY clause cannot contain window functions!\n" +
        "LINE 1: ...ulation AS (SELECT position",
    );
    expect(summary).toBe("Binder Error: GROUP BY clause cannot contain window functions!");
    expect(detail).toBe("LINE 1: ...ulation AS (SELECT position");
  });

  it("returns an empty detail when the cleaned message has no continuation", () => {
    const { summary, detail } = splitStepError("executing query: engine: query: nope");
    expect(summary).toBe("nope");
    expect(detail).toBe("");
  });
});
