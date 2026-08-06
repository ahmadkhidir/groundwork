import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ReconciliationNotice } from "./ReconciliationNotice";

describe("ReconciliationNotice", () => {
  it("renders nothing when there is nothing to disclose (no warnings, no dropped rows)", () => {
    const { container } = render(
      <ReconciliationNotice
        summary={{ rows_in: 100, rows_out: 100, exclusion_warnings: [] }}
      />,
    );
    expect(container.firstChild).toBeNull();
    expect(screen.queryByText("Reconciliation note")).not.toBeInTheDocument();
  });

  it("discloses dropped rows against rows read", () => {
    render(
      <ReconciliationNotice
        summary={{ rows_in: 1_000, rows_out: 690, exclusion_warnings: [] }}
      />,
    );
    expect(screen.getByText("Reconciliation note")).toBeInTheDocument();
    expect(
      screen.getByText(/covers 690 of 1,000 rows read\./),
    ).toBeInTheDocument();
  });

  it("surfaces each exclusion warning unprompted", () => {
    render(
      <ReconciliationNotice
        summary={{
          rows_in: 1_000,
          rows_out: 1_000,
          exclusion_warnings: ["excluded 31% of rows where region was null"],
        }}
      />,
    );
    expect(
      screen.getByText("excluded 31% of rows where region was null"),
    ).toBeInTheDocument();
  });

  it("tolerates a null warnings list from the API without crashing (regression)", () => {
    // A nil Go slice marshals to JSON `null`; the render must treat it as "no warnings". This is
    // the exact shape that previously threw console "Cannot read properties of null".
    render(
      <ReconciliationNotice
        summary={{
          rows_in: 1_000,
          rows_out: 690,
          exclusion_warnings: null as unknown as string[],
        }}
      />,
    );
    expect(screen.getByText("Reconciliation note")).toBeInTheDocument();
    expect(
      screen.getByText(/covers 690 of 1,000 rows read\./),
    ).toBeInTheDocument();
  });
});