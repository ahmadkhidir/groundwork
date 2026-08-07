import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UsageTab } from "./UsageTab";
import { ApiError, api } from "@/lib/api-client";
import { SubscriptionTier, type UsageRecord } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

function usageResponse(records: UsageRecord[] = []) {
  const latest = records[0];
  return {
    usage: {
      records,
      totals: latest
        ? {
            engine_seconds: latest.engine_seconds,
            storage_bytes: latest.storage_bytes,
            model_tokens: latest.model_tokens,
          }
        : { engine_seconds: 0, storage_bytes: 0, model_tokens: 0 },
    },
  };
}

const RECORDS: UsageRecord[] = [
  {
    id: "u-2",
    workspace_id: "ws-1",
    period_start: "2026-08-01T00:00:00Z",
    period_end: "2026-08-07T00:00:00Z",
    engine_seconds: 42.5,
    storage_bytes: 2 * 1024 * 1024,
    model_tokens: 1200,
  },
];

describe("UsageTab", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  it("renders loading then the current-period stat rows (FEAT-047 AC1)", async () => {
    getMock.mockResolvedValue(usageResponse(RECORDS));
    render(<UsageTab workspaceId="ws-1" tier={SubscriptionTier.Free} />);

    await waitFor(() => expect(screen.getByText("Engine time")).toBeInTheDocument());
    expect(screen.getByText("42.5s")).toBeInTheDocument();
    expect(screen.getByText("Storage")).toBeInTheDocument();
    expect(screen.getByText("2.0 MB")).toBeInTheDocument();
    expect(screen.getByText("Model tokens")).toBeInTheDocument();
    expect(screen.getByText("1,200")).toBeInTheDocument();
  });

  it("Free tier shows totals only — the breakdown is hidden (monetization.md)", async () => {
    getMock.mockResolvedValue(usageResponse(RECORDS));
    render(<UsageTab workspaceId="ws-1" tier={SubscriptionTier.Free} />);

    await waitFor(() => expect(screen.getByText("Engine time")).toBeInTheDocument());
    expect(screen.queryByText(/View period breakdown/)).not.toBeInTheDocument();
  });

  it("Pro tier shows the per-period breakdown", async () => {
    getMock.mockResolvedValue(usageResponse(RECORDS));
    const user = userEvent.setup();
    render(<UsageTab workspaceId="ws-1" tier={SubscriptionTier.Pro} />);

    await waitFor(() => expect(screen.getByText("Engine time")).toBeInTheDocument());
    await user.click(screen.getByText(/View period breakdown/));

    expect(screen.getByText("Aug 2026")).toBeInTheDocument();
    // The breakdown cell's engine seconds render as "42.5s" across two text nodes, so the value is
    // matched as a substring of the cell's text.
    expect(screen.getAllByText(/42\.5/).length).toBeGreaterThan(0);
  });

  it("shows an honest empty state when nothing is recorded yet", async () => {
    getMock.mockResolvedValue(usageResponse([]));
    render(<UsageTab workspaceId="ws-1" tier={SubscriptionTier.Free} />);

    await waitFor(() =>
      expect(screen.getByText(/No usage recorded yet/)).toBeInTheDocument(),
    );
  });

  it("shows the shared error state with retry", async () => {
    getMock.mockRejectedValue(new ApiError(500, "Something went wrong. Try again.", "internal_error"));
    render(<UsageTab workspaceId="ws-1" tier={SubscriptionTier.Free} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument());
  });
});
