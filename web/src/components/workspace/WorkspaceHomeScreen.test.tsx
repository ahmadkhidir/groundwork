import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceHomeScreen } from "./WorkspaceHomeScreen";
import { ApiError, api } from "@/lib/api-client";
import { EngineStatus } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

function workspaceWith(engineStatus: EngineStatus, hasDatasets = false) {
  return {
    id: "ws-1",
    name: "Q3 Numbers",
    owner_id: "user-1",
    engine_status: engineStatus,
    has_datasets: hasDatasets,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

describe("WorkspaceHomeScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows a neutral 'checking status' pill before the first fetch resolves", async () => {
    let resolveGet: (value: { workspace: ReturnType<typeof workspaceWith> }) => void;
    getMock.mockReturnValue(
      new Promise((resolve) => {
        resolveGet = resolve;
      }),
    );

    render(<WorkspaceHomeScreen workspaceId="ws-1" />);
    expect(screen.getByText("Checking status…")).toBeInTheDocument();

    resolveGet!({ workspace: workspaceWith(EngineStatus.Sleeping) });
    await waitFor(() => expect(screen.getByText("Sleeping")).toBeInTheDocument());
  });

  it("renders the workspace name and the honest engine status pill once loaded", async () => {
    getMock.mockResolvedValue({ workspace: workspaceWith(EngineStatus.Ready) });

    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.getByText("Ready")).toBeInTheDocument();
  });

  it("shows the error state with a working retry when the initial fetch fails", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(screen.getByText("Something went wrong")).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce({ workspace: workspaceWith(EngineStatus.Sleeping) });
    screen.getByRole("button", { name: "Try Again" }).click();

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
  });

  it("polls for live engine status and reflects a waking → ready transition", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    // Path-dispatched rather than a blind call-order queue: SemanticLayerSummaryPanel (FEAT-019)
    // now issues its own concurrent api.get for /semantic-layer/summary, which would otherwise
    // consume one of a sequential .mockResolvedValueOnce chain out of order.
    let workspaceFetchCount = 0;
    const workspaceSequence = [
      workspaceWith(EngineStatus.Sleeping),
      workspaceWith(EngineStatus.Waking),
      workspaceWith(EngineStatus.Ready),
    ];
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/semantic-layer/summary")) {
        return { relationship_count: 0, metric_definition_count: 0 };
      }
      const workspace = workspaceSequence[Math.min(workspaceFetchCount, workspaceSequence.length - 1)];
      workspaceFetchCount += 1;
      return { workspace };
    });

    render(<WorkspaceHomeScreen workspaceId="ws-1" />);
    await vi.waitFor(() => expect(screen.getByText("Sleeping")).toBeInTheDocument());

    await vi.advanceTimersByTimeAsync(10_000);
    await vi.waitFor(() => expect(screen.getByText("Waking")).toBeInTheDocument());

    await vi.advanceTimersByTimeAsync(10_000);
    await vi.waitFor(() => expect(screen.getByText("Ready")).toBeInTheDocument());

    expect(workspaceFetchCount).toBe(3);
  });

  it("keeps the last-known status visible if a later poll fails, rather than erroring out", async () => {
    let workspaceFetchCount = 0;
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/semantic-layer/summary")) {
        return { relationship_count: 0, metric_definition_count: 0 };
      }
      workspaceFetchCount += 1;
      if (workspaceFetchCount === 1) return { workspace: workspaceWith(EngineStatus.Ready) };
      throw new ApiError(500, "Transient");
    });

    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);
    await vi.waitFor(() => expect(screen.getByText("Ready")).toBeInTheDocument());

    await vi.advanceTimersByTimeAsync(10_000);

    // Still showing the last-known loaded screen, not the error state.
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.queryByText("Something went wrong")).not.toBeInTheDocument();
  });

  it("renders the Ask panel as visibly disabled rather than silently inert", async () => {
    getMock.mockResolvedValue({ workspace: workspaceWith(EngineStatus.Ready) });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.getByPlaceholderText("Ask a question about your data...")).toBeDisabled();
    expect(screen.getByText("Asking questions arrives in Phase 2 (FEAT-013).")).toBeInTheDocument();
  });

  it("renders the honest empty state for recent reports and the real semantic layer summary counts", async () => {
    // FEAT-019 landed GET /v1/workspaces/:id/semantic-layer/summary — distinguish it from the
    // workspace fetch by path so SemanticLayerSummaryPanel gets its own real response shape rather
    // than the workspace object.
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/semantic-layer/summary")) {
        return { relationship_count: 12, metric_definition_count: 4 };
      }
      return { workspace: workspaceWith(EngineStatus.Ready) };
    });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.getByText("Your reports will appear here.")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByText("12 relationships, 4 metric definitions")).toBeInTheDocument(),
    );
  });

  it("links Upload data to the File Upload screen", async () => {
    getMock.mockResolvedValue({ workspace: workspaceWith(EngineStatus.Ready) });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.getByRole("link", { name: /Upload data/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1/upload",
    );
  });

  // screens/04-workspace-home.md: "Upload data prompt... Shown when no files ingested yet."
  it("offers the Upload data CTA while the workspace has no datasets", async () => {
    getMock.mockResolvedValue({
      workspace: workspaceWith(EngineStatus.Sleeping, false),
    });

    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(screen.getByRole("link", { name: /upload data/i })).toBeInTheDocument(),
    );
  });

  it("hides the Upload data CTA once the workspace has datasets", async () => {
    getMock.mockResolvedValue({
      workspace: workspaceWith(EngineStatus.Ready, true),
    });

    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.queryByRole("link", { name: /upload data/i })).not.toBeInTheDocument();
  });
});
