import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SharedReportScreen } from "./SharedReportScreen";
import { ApiError, api } from "@/lib/api-client";
import { loginWithRedirect } from "@/lib/routes";
import type { ExecuteStepOutput } from "@/components/run/step-payloads";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

const REPORT = {
  id: "report-1",
  title: "Revenue by region",
  narrative: "Revenue reached $1.2M in Q3.",
  chart_spec: {
    chart_type: "bar" as const,
    x_axis_column: "region",
    y_axis_column: "revenue",
    series_column: "",
    title: "Revenue by region",
  },
  provenance_links: [
    {
      id: "claim-1",
      report_id: "report-1",
      claim_text: "$1.2M",
      stated_value: "$1.2M",
      source_query: "SELECT SUM(revenue) ...",
      source_column: "revenue",
      source_row_ids: ["0"],
    },
  ],
  reconciliation_summary: { rows_in: 100, rows_out: 90, exclusion_warnings: [] },
  created_at: "2026-08-01T00:00:00Z",
};

const EXECUTE: ExecuteStepOutput = {
  columns: ["region", "revenue"],
  rows: [
    ["North", 1200000],
    ["South", 400000],
  ],
  row_count: 2,
};

describe("SharedReportScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  it("renders the report content read-only without Export or Ask actions (screens/09 Read-Only state)", async () => {
    getMock.mockResolvedValue({ report: REPORT, execute_result: EXECUTE });
    render(<SharedReportScreen shareToken="token-1" />);

    await waitFor(() => expect(screen.getByRole("heading", { level: 1, name: "Revenue by region" })).toBeInTheDocument());

    // Same content: narrative, chart, accessible data table.
    expect(screen.getByText(/Revenue reached/)).toBeInTheDocument();
    expect(screen.getByText("View as table")).toBeInTheDocument();

    // Interact actions hidden/replaced with the sign-in prompt.
    expect(screen.getByRole("link", { name: "Sign in to interact" })).toHaveAttribute(
      "href",
      loginWithRedirect(window.location.pathname),
    );
    expect(screen.queryByRole("button", { name: "Export" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Ask a follow-up" })).not.toBeInTheDocument();
  });

  it("clicking a provenance figure shows a sign-in hint instead of the authenticated panel", async () => {
    getMock.mockResolvedValue({ report: REPORT, execute_result: EXECUTE });
    const user = userEvent.setup();
    render(<SharedReportScreen shareToken="token-1" />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Show provenance/ })).toBeInTheDocument(),
    );

    await user.click(screen.getByRole("button", { name: /Show provenance/ }));

    expect(screen.getByText(/Sign in to view the source data/)).toBeInTheDocument();
  });

  it("renders narrative without a chart when the execute facts are gone (honest degradation)", async () => {
    getMock.mockResolvedValue({ report: REPORT, execute_result: null });
    render(<SharedReportScreen shareToken="token-1" />);

    await waitFor(() =>
      expect(screen.getByRole("heading", { level: 1, name: "Revenue by region" })).toBeInTheDocument(),
    );
    expect(screen.getByText(/Revenue reached/)).toBeInTheDocument();
    expect(screen.queryByText("View as table")).not.toBeInTheDocument();
  });

  it("shows the dedicated not-found state for a revoked or unknown link (FEAT-046 revoke)", async () => {
    getMock.mockRejectedValue(
      new ApiError(
        404,
        "This shared report link is no longer available or has been revoked.",
        "share_not_found",
      ),
    );
    render(<SharedReportScreen shareToken="revoked-token" />);

    await waitFor(() =>
      expect(screen.getByText(/no longer available or has been revoked/)).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument();
  });

  it("shows the shared error state with retry for a server failure", async () => {
    getMock.mockRejectedValue(new ApiError(500, "Something went wrong. Try again.", "internal_error"));
    render(<SharedReportScreen shareToken="token-1" />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument());
  });
});
