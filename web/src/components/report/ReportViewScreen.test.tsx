import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ReportViewScreen } from "./ReportViewScreen";
import { ApiError, api } from "@/lib/api-client";
import { SubscriptionTier, type Report } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

const { pushMock, replaceMock, searchParamsState } = vi.hoisted(() => ({
  pushMock: vi.fn(),
  replaceMock: vi.fn(),
  searchParamsState: { current: new URLSearchParams() },
}));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, replace: replaceMock }),
  useSearchParams: () => searchParamsState.current,
}));

function report(overrides: Partial<Report> = {}): Report {
  return {
    id: "report-1",
    workspace_id: "ws-1",
    run_id: "run-1",
    title: "Customers by region",
    narrative: "North leads the count with 10 customers.",
    chart_spec: {
      chart_type: "bar",
      x_axis_column: "region",
      y_axis_column: "cnt",
      title: "Customers by region",
    },
    provenance_links: [],
    reconciliation_summary: { rows_in: 100, rows_out: 100, exclusion_warnings: [] },
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const EXECUTE_STEP = {
  id: "step-execute",
  run_id: "run-1",
  step_name: "execute" as const,
  status: "complete" as const,
  idempotency_key: "run-1:execute",
  output: JSON.stringify({
    columns: ["region", "cnt"],
    rows: [["North", 10], ["South", 8]],
    row_count: 2,
  }),
};

async function renderLoadedScreen(props?: Partial<React.ComponentProps<typeof ReportViewScreen>>) {
  getMock.mockImplementation(async (path: string) => {
    if (path.includes("/reports/")) return { report: report() };
    return { run: { id: "run-1" }, steps: [EXECUTE_STEP] };
  });
  const utils = render(
    <ReportViewScreen
      workspaceId="ws-1"
      reportId="report-1"
      {...props}
    />,
  );
  await screen.findByRole("heading", { level: 1, name: "Customers by region" });
  return utils;
}

describe("ReportViewScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    pushMock.mockReset();
    replaceMock.mockReset();
    searchParamsState.current = new URLSearchParams();
  });

  it("renders the report title, narrative, chart, and source-run links once loaded", async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/reports/")) return { report: report() };
      return { run: { id: "run-1" }, steps: [EXECUTE_STEP] };
    });
    render(<ReportViewScreen workspaceId="ws-1" reportId="report-1" />);

    await screen.findByRole("heading", { level: 1, name: "Customers by region" });
    expect(screen.getByText(/North leads the count/)).toBeInTheDocument();
    expect(screen.getByTestId("chart-renderer")).toBeInTheDocument();
    // "View source run" exists twice: the reading-column link (mobile/tablet) and the web side
    // action panel. Both must route to the source run.
    const sourceRunLinks = screen.getAllByRole("link", { name: /View source run/ });
    expect(sourceRunLinks.length).toBeGreaterThan(0);
    for (const link of sourceRunLinks) {
      expect(link).toHaveAttribute("href", "/workspaces/ws-1/runs/run-1");
    }
    // The data table hides behind a closed disclosure until opened.
    const disclosure = screen.getByText("View as table").closest("details");
    expect(disclosure?.open).toBe(false);
  });

  it("shows the error state with a working retry when the report fetch fails", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<ReportViewScreen workspaceId="ws-1" reportId="report-1" />);

    await screen.findByText("Server exploded");
    getMock.mockImplementation(async (path: string) =>
      path.includes("/reports/") ? { report: report() } : { run: { id: "run-1" }, steps: [] },
    );
    screen.getByRole("button", { name: "Try Again" }).click();

    await screen.findByRole("heading", { level: 1, name: "Customers by region" });
  });

  it("falls back to a note plus table when the chart type is unsupported", async () => {
    getMock.mockImplementation(async (path: string) =>
      path.includes("/reports/")
        ? {
            report: report({
              chart_spec: {
                chart_type: "heatmap" as unknown as Report["chart_spec"]["chart_type"],
                x_axis_column: "region",
                y_axis_column: "cnt",
                title: "Heatmap",
              },
            }),
          }
        : { run: { id: "run-1" }, steps: [EXECUTE_STEP] },
    );
    render(<ReportViewScreen workspaceId="ws-1" reportId="report-1" />);

    await screen.findByText(/isn't supported yet/);
    expect(screen.queryByTestId("chart-renderer")).not.toBeInTheDocument();
  });

  it("renders the reconciliation notice when the report discloses exclusions", async () => {
    getMock.mockImplementation(async (path: string) =>
      path.includes("/reports/")
        ? {
            report: report({
              reconciliation_summary: {
                rows_in: 1_000,
                rows_out: 690,
                exclusion_warnings: ["excluded 31% of rows where region was null"],
              },
            }),
          }
        : { run: { id: "run-1" }, steps: [] },
    );
    render(<ReportViewScreen workspaceId="ws-1" reportId="report-1" />);

    await screen.findByText("Reconciliation note");
    expect(screen.getByText(/covers 690 of 1,000 rows read\./)).toBeInTheDocument();
  });

  it("does not show the reconciliation notice when nothing was excluded", async () => {
    getMock.mockImplementation(async (path: string) =>
      path.includes("/reports/") ? { report: report() } : { run: { id: "run-1" }, steps: [] },
    );
    render(<ReportViewScreen workspaceId="ws-1" reportId="report-1" />);

    await screen.findByRole("heading", { level: 1, name: "Customers by region" });
    expect(screen.queryByText("Reconciliation note")).not.toBeInTheDocument();
  });

  it("renders the report even when the run fetch fails (narrative is self-contained)", async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/reports/")) return { report: report() };
      throw new ApiError(500, "Run fetch failed");
    });
    render(<ReportViewScreen workspaceId="ws-1" reportId="report-1" />);

    await screen.findByRole("heading", { level: 1, name: "Customers by region" });
    expect(screen.getByText(/North leads the count/)).toBeInTheDocument();
  });

  it("opens the Export Dialog via ?export= URL state from the web action panel", async () => {
    await renderLoadedScreen();
    const panel = screen.getByTestId("report-action-panel");
    await userEvent.click(within(panel).getByRole("button", { name: "Export" }));
    expect(pushMock).toHaveBeenCalledWith(
      "/workspaces/ws-1/reports/report-1?export=1",
    );
  });

  it("renders the mobile bottom action bar with Export and Ask a follow-up", async () => {
    await renderLoadedScreen();
    const bar = screen.getByTestId("report-mobile-actions");
    const exportButton = within(bar).getByRole("button", { name: "Export" });
    const followUpButton = within(bar).getByRole("button", { name: "Ask a follow-up" });
    // FEAT-041: 44px touch targets on mobile (the .touch-target utility guarantees the hit area).
    expect(exportButton.className).toContain("touch-target");
    expect(followUpButton.className).toContain("touch-target");
  });

  it("renders the Export Dialog when ?export= is in the URL", async () => {
    searchParamsState.current = new URLSearchParams("export=1");
    await renderLoadedScreen();
    expect(
      screen.getByRole("heading", { name: "Export report" }),
    ).toBeInTheDocument();
  });

  it("does not render the Export Dialog without the export param", async () => {
    await renderLoadedScreen();
    expect(
      screen.queryByRole("heading", { name: "Export report" }),
    ).not.toBeInTheDocument();
  });

  it("passes the display tier through to the Export Dialog (Pro unlocks Excel/CSV)", async () => {
    searchParamsState.current = new URLSearchParams("export=1");
    await renderLoadedScreen({ userTier: SubscriptionTier.Pro });
    expect(screen.getByRole("radio", { name: /Excel\/CSV/ })).toBeInTheDocument();
  });
});
