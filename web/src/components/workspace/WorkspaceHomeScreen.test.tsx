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

// AskPanel uses next/navigation's useRouter to navigate to the created run; the workspace screen
// test renders it inside WorkspaceHomeScreen, so the router must be present.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

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
      // RecentReportsPanel (FEAT-034) issues its own /reports fetch; it must not consume a step of
      // the workspace status sequence.
      if (path.includes("/reports")) {
        return { reports: [] };
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
      // RecentReportsPanel (FEAT-034) issues its own /reports fetch; it must not consume a step of
      // the workspace status sequence.
      if (path.includes("/reports")) {
        return { reports: [] };
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

  it("renders the Ask panel as a live question input (FEAT-013)", async () => {
    getMock.mockResolvedValue({ workspace: workspaceWith(EngineStatus.Ready) });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.getByPlaceholderText("Ask a question about your data...")).toBeEnabled();
  });

  it("renders the honest empty state for recent reports and the real semantic layer summary counts", async () => {
    // FEAT-019 landed GET /v1/workspaces/:id/semantic-layer/summary — distinguish it from the
    // workspace fetch by path so SemanticLayerSummaryPanel gets its own real response shape rather
    // than the workspace object.
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/semantic-layer/summary")) {
        return { relationship_count: 12, metric_definition_count: 4 };
      }
      // RecentReportsPanel (FEAT-034) issues its own /reports fetch; give it its real empty shape.
      if (path.includes("/reports")) {
        return { reports: [] };
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

  /**
   * Walks up from an element checking whether any ancestor carries Tailwind's `hidden` class (a
   * below-lg `display: none`). jsdom cannot evaluate breakpoints, so this guards the
   * mobile-visibility regression structurally: per screens/04-workspace-home.md (updated by
   * product-shaper), BOTH the Datasets panel ("Both (collapsible on mobile)") and the Semantic
   * layer summary ("Both (compact on mobile)") render on every viewport — neither may sit in a
   * hidden below-lg container anymore.
   */
  function isInHiddenAncestor(el: Element | null): boolean {
    let node: Element | null = el;
    while (node) {
      if (typeof node.className === "string" && node.className.split(/\s+/).includes("hidden")) {
        return true;
      }
      node = node.parentElement;
    }
    return false;
  }

  // FEAT-003 / FEAT-049: adding more data to a workspace that already has some is an ordinary,
  // required action — the empty-state "Upload data" CTA is gone once has_datasets is true, so the
  // Datasets panel's "Add data" affordance is what keeps File Upload reachable (and it must be
  // reachable on mobile too, per the panel's "Both (collapsible on mobile)" platform tag).
  it("keeps Add data reachable after datasets exist, on every viewport", async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/semantic-layer/summary")) {
        return { relationship_count: 12, metric_definition_count: 4 };
      }
      if (path.includes("/reports")) {
        return { reports: [] };
      }
      return { workspace: workspaceWith(EngineStatus.Ready, true) };
    });
    render(<WorkspaceHomeScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    const addDataLink = screen.getByRole("link", { name: /add data/i });
    expect(addDataLink).toHaveAttribute("href", "/workspaces/ws-1/upload");
    // Mobile-visibility regression guards: neither Add data nor the semantic layer summary may sit
    // inside a `hidden` (below-lg) container — screens/04 now tags both panels "Both".
    expect(isInHiddenAncestor(addDataLink)).toBe(false);
    await waitFor(() => expect(screen.getByText(/relationships/)).toBeInTheDocument());
    expect(isInHiddenAncestor(screen.getByText(/relationships/))).toBe(false);
  });

  // screens/04's read-only edge case (FEAT-045): the Upload CTA is hidden for read-only members —
  // the persistent "Add data" affordance follows the same rule.
  it("hides the Add data affordance for read-only shared access", async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path.includes("/semantic-layer/summary")) {
        return { relationship_count: 0, metric_definition_count: 0 };
      }
      if (path.includes("/reports")) {
        return { reports: [] };
      }
      return { workspace: workspaceWith(EngineStatus.Ready, true) };
    });
    render(<WorkspaceHomeScreen workspaceId="ws-1" userId="other-user" />);

    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());
    expect(screen.queryByRole("link", { name: /add data/i })).not.toBeInTheDocument();
  });
});
