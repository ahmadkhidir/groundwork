import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceListScreen } from "./WorkspaceListScreen";
import { ApiError, api } from "@/lib/api-client";
import { EngineStatus } from "@/types/schema";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), post: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);

const workspace = {
  id: "ws-1",
  name: "Q3 Numbers",
  owner_id: "user-1",
  engine_status: EngineStatus.Sleeping,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("WorkspaceListScreen", () => {
  beforeEach(() => {
    pushMock.mockClear();
    getMock.mockReset();
    postMock.mockReset();
  });

  it("shows a skeleton while loading, then the loaded grid", async () => {
    let resolveList: (value: { workspaces: typeof workspace[] }) => void;
    getMock.mockReturnValue(
      new Promise((resolve) => {
        resolveList = resolve;
      }),
    );

    render(<WorkspaceListScreen />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();

    resolveList!({ workspaces: [workspace] });
    await waitFor(() =>
      expect(screen.getByText("Q3 Numbers")).toBeInTheDocument(),
    );
  });

  it("shows the empty state for a brand-new account", async () => {
    getMock.mockResolvedValue({ workspaces: [] });
    render(<WorkspaceListScreen />);

    await waitFor(() =>
      expect(screen.getByText("No workspaces yet")).toBeInTheDocument(),
    );
    expect(
      screen.getByRole("button", { name: "Create your first workspace" }),
    ).toBeInTheDocument();
  });

  it("shows the error state with a working retry on fetch failure", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<WorkspaceListScreen />);

    await waitFor(() =>
      expect(screen.getByText("Something went wrong")).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce({ workspaces: [workspace] });
    await userEvent.click(screen.getByRole("button", { name: "Try Again" }));

    await waitFor(() =>
      expect(screen.getByText("Q3 Numbers")).toBeInTheDocument(),
    );
  });

  it("creates a workspace and navigates to it", async () => {
    getMock.mockResolvedValue({ workspaces: [workspace] });
    postMock.mockResolvedValue({ workspace: { ...workspace, id: "ws-2", name: "New One" } });

    render(<WorkspaceListScreen />);
    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    await userEvent.type(screen.getByLabelText("Workspace name"), "New One");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-2"));
  });

  it("surfaces the tier-gate message inline when the workspace limit is reached", async () => {
    getMock.mockResolvedValue({ workspaces: [workspace] });
    postMock.mockRejectedValue(
      new ApiError(403, "You've reached your plan's workspace limit.", "workspace_limit_reached"),
    );

    render(<WorkspaceListScreen />);
    await waitFor(() => expect(screen.getByText("Q3 Numbers")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    await userEvent.type(screen.getByLabelText("Workspace name"), "One too many");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(
        screen.getByText("You've reached your plan's workspace limit."),
      ).toBeInTheDocument(),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });
});
