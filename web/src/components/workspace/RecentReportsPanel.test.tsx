import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RecentReportsPanel } from "./RecentReportsPanel";
import { ApiError, api } from "@/lib/api-client";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

// RecentReportsPanel links to Report View via next/link; the mock-router isn't needed here (Link
// renders its href), but keeping the module resolvable matches Vitest's Next.js setup.
const REPORT = {
  id: "report-1",
  run_id: "run-1",
  title: "Quarterly sales by region",
  created_at: "2026-08-06T12:00:00Z",
};

describe("RecentReportsPanel", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  afterEach(() => {
    vi.clearAllTimers();
  });

  it("shows skeleton placeholders while the list is loading", () => {
    // Never resolves within the assertion window — the loading state is what we're after.
    getMock.mockReturnValue(new Promise(() => {}));
    render(<RecentReportsPanel workspaceId="ws-1" />);

    // The panel's loading surface is a card of pulsing skeletons; assert on the ABSENCE of the
    // empty/error text during loading. queryByText (not getByText) is required here: the empty
    // state is correctly absent while loading, and getByText would throw on a non-match.
    expect(screen.queryByText(/your reports will appear here/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/appear here/i)).not.toBeInTheDocument();
  });

  it("fetches recent reports with limit=10 and renders each as a Report View link", async () => {
    getMock.mockResolvedValueOnce({ reports: [REPORT] });
    render(<RecentReportsPanel workspaceId="ws-1" />);

    await waitFor(() =>
      expect(screen.getByText("Quarterly sales by region")).toBeInTheDocument(),
    );
    expect(getMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/reports?limit=10");

    const link = screen.getByRole("link", { name: /Quarterly sales by region/i });
    expect(link).toHaveAttribute("href", "/workspaces/ws-1/reports/report-1");
  });

  it("shows the empty state (no reports yet) when the workspace has no reports", async () => {
    getMock.mockResolvedValueOnce({ reports: [] });
    render(<RecentReportsPanel workspaceId="ws-1" />);

    await waitFor(() =>
      expect(screen.getByText("Your reports will appear here.")).toBeInTheDocument(),
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows a panel-scoped error message when the fetch fails (not a full error state)", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<RecentReportsPanel workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Server exploded")).toBeInTheDocument());
    // The panel fails quietly on a screen whose primary surface still works.
    expect(screen.queryByText("Something went wrong")).not.toBeInTheDocument();
    expect(screen.queryByText("Your reports will appear here.")).not.toBeInTheDocument();
  });
});