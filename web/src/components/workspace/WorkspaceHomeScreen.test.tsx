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

function workspaceWith(engineStatus: EngineStatus) {
  return {
    id: "ws-1",
    name: "Q3 Numbers",
    owner_id: "user-1",
    engine_status: engineStatus,
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
    getMock
      .mockResolvedValueOnce({ workspace: workspaceWith(EngineStatus.Sleeping) })
      .mockResolvedValueOnce({ workspace: workspaceWith(EngineStatus.Waking) })
      .mockResolvedValue({ workspace: workspaceWith(EngineStatus.Ready) });

    render(<WorkspaceHomeScreen workspaceId="ws-1" />);
    await vi.waitFor(() => expect(screen.getByText("Sleeping")).toBeInTheDocument());

    await vi.advanceTimersByTimeAsync(10_000);
    await vi.waitFor(() => expect(screen.getByText("Waking")).toBeInTheDocument());

    await vi.advanceTimersByTimeAsync(10_000);
    await vi.waitFor(() => expect(screen.getByText("Ready")).toBeInTheDocument());

    expect(getMock).toHaveBeenCalledTimes(3);
  });

  it("keeps the last-known status visible if a later poll fails, rather than erroring out", async () => {
    getMock
      .mockResolvedValueOnce({ workspace: workspaceWith(EngineStatus.Ready) })
      .mockRejectedValueOnce(new ApiError(500, "Transient"));

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

  it("renders the honest empty state for recent reports and the semantic layer summary", async () => {
    getMock.mockResolvedValue({ workspace: workspaceWith(EngineStatus.Ready) });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.getByText("Your reports will appear here.")).toBeInTheDocument();
    expect(screen.getByText("Not built yet")).toBeInTheDocument();
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
});
