import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceSwitcher } from "./WorkspaceSwitcher";
import { ApiError, api } from "@/lib/api-client";
import { EngineStatus } from "@/types/schema";

vi.mock("next/navigation", () => ({
  usePathname: () => "/workspaces/ws-1",
}));

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

function workspace(id: string, name: string) {
  return {
    id,
    name,
    owner_id: "user-1",
    engine_status: EngineStatus.Sleeping,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

describe("WorkspaceSwitcher", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  it("shows the current workspace's name once loaded", async () => {
    getMock.mockResolvedValue({ workspaces: [workspace("ws-1", "Q3 Numbers")] });
    render(<WorkspaceSwitcher currentWorkspaceId="ws-1" />);

    expect(await screen.findByText("Q3 Numbers")).toBeInTheDocument();
  });

  it("opens a menu listing every workspace plus the All workspaces link", async () => {
    getMock.mockResolvedValue({
      workspaces: [workspace("ws-1", "Q3 Numbers"), workspace("ws-2", "HR Data")],
    });
    const user = userEvent.setup();
    render(<WorkspaceSwitcher currentWorkspaceId="ws-1" />);

    await screen.findByText("Q3 Numbers");
    await user.click(screen.getByRole("button", { name: /Q3 Numbers/ }));

    const menu = screen.getByRole("menu", { name: "Switch workspace" });
    expect(menu).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /Q3 Numbers/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1",
    );
    expect(screen.getByRole("menuitem", { name: /HR Data/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-2",
    );
    expect(screen.getByRole("menuitem", { name: /All workspaces/ })).toHaveAttribute(
      "href",
      "/workspaces",
    );
  });

  it("marks the current workspace as Current and closes the menu after choosing", async () => {
    getMock.mockResolvedValue({
      workspaces: [workspace("ws-1", "Q3 Numbers"), workspace("ws-2", "HR Data")],
    });
    const user = userEvent.setup();
    render(<WorkspaceSwitcher currentWorkspaceId="ws-1" />);

    await screen.findByText("Q3 Numbers");
    await user.click(screen.getByRole("button", { name: /Q3 Numbers/ }));

    const currentItem = screen.getByRole("menuitem", { name: /Q3 Numbers/ });
    expect(currentItem).toHaveAttribute("aria-current", "page");
    expect(currentItem).toHaveTextContent("Current");

    await user.click(screen.getByRole("menuitem", { name: /HR Data/ }));
    expect(screen.queryByRole("menu", { name: "Switch workspace" })).not.toBeInTheDocument();
  });

  it("falls back to a neutral label when there is no current workspace", async () => {
    getMock.mockResolvedValue({ workspaces: [workspace("ws-1", "Q3 Numbers")] });
    render(<WorkspaceSwitcher />);

    expect(await screen.findByText("Select workspace")).toBeInTheDocument();
  });

  it("surfaces fetch failure honestly as a link to the Workspace List", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    const user = userEvent.setup();
    render(<WorkspaceSwitcher currentWorkspaceId="ws-1" />);

    await user.click(await screen.findByRole("button", { name: /Workspaces/ }));
    expect(screen.getByRole("menuitem", { name: /Workspace list unavailable/ })).toHaveAttribute(
      "href",
      "/workspaces",
    );
  });

  it("closes the menu on Escape", async () => {
    getMock.mockResolvedValue({
      workspaces: [workspace("ws-1", "Q3 Numbers"), workspace("ws-2", "HR Data")],
    });
    const user = userEvent.setup();
    render(<WorkspaceSwitcher currentWorkspaceId="ws-1" />);

    await screen.findByText("Q3 Numbers");
    await user.click(screen.getByRole("button", { name: /Q3 Numbers/ }));
    expect(screen.getByRole("menu", { name: "Switch workspace" })).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "Switch workspace" })).not.toBeInTheDocument();
  });
});
