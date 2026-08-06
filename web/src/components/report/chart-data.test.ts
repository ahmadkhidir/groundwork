import { describe, expect, it } from "vitest";
import {
  displayValue,
  isSupportedChartType,
  resolveColumnIndex,
  toNumber,
} from "./chart-data";

describe("resolveColumnIndex", () => {
  it("returns the index of a present column", () => {
    expect(resolveColumnIndex(["region", "cnt"], "cnt")).toBe(1);
  });

  it("returns -1 for an absent column or an empty name", () => {
    expect(resolveColumnIndex(["region"], "nope")).toBe(-1);
    expect(resolveColumnIndex(["region"], "")).toBe(-1);
  });
});

describe("toNumber", () => {
  it("accepts finite numbers and numeric strings", () => {
    expect(toNumber(10)).toBe(10);
    expect(toNumber("42")).toBe(42);
    expect(toNumber(" 3.5 ")).toBe(3.5);
  });

  it("rejects non-numeric and null values", () => {
    expect(toNumber(null)).toBeNull();
    expect(toNumber("")).toBeNull();
    expect(toNumber(" ")).toBeNull();
    expect(toNumber("North")).toBeNull();
    expect(toNumber(true)).toBeNull();
    expect(toNumber(undefined)).toBeNull();
  });
});

describe("displayValue", () => {
  it("renders null/undefined as the visible dash", () => {
    expect(displayValue(null)).toBe("—");
    expect(displayValue(undefined)).toBe("—");
  });

  it("stringifies scalars", () => {
    expect(displayValue("North")).toBe("North");
    expect(displayValue(10)).toBe("10");
    expect(displayValue(false)).toBe("false");
  });
});

describe("isSupportedChartType", () => {
  it("accepts the four drawn chart types", () => {
    for (const type of ["bar", "line", "pie", "scatter"]) {
      expect(isSupportedChartType(type)).toBe(true);
    }
  });

  it("rejects unknown/future types so they fall back to the table", () => {
    expect(isSupportedChartType("heatmap")).toBe(false);
    expect(isSupportedChartType("table")).toBe(false);
  });
});