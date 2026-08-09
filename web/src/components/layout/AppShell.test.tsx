import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppShell } from "./AppShell";

// The bell is part of the shell, but its polling is covered by NotificationsBell's own contract;
// the shell test only needs to prove the bell is mounted, so the fetch is mocked at the seam.
vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

import { api } from "@/lib/api-client";
const getMock = vi.mocked(api.get);

const pathnameMock = vi.fn(() => "/workspaces/ws-1");
vi.mock("next/navigation", () => ({
  usePathname: () => pathnameMock(),
}));

/** The WorkspaceList GET the switcher performs on mount. */
function listResponse() {
  return {
    workspaces: [
      {
        id: "ws-1",
        name: "Q3 Numbers",
        owner_id: "user-1",
        engine_status: "sleeping",
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      },
    ],
  };
}

/**
 * The Navigation Shell (screens.md): the sticky Navbar shown on every authenticated screen —
 * brand lockup, workspace switcher, notifications bell, and the account menu. Within a workspace
 * the collapsible Sidebar renders beside the content (desktop); on mobile a hamburger opens the
 * Drawer with the switcher and the workspace links.
 */
describe("AppShell", () => {
  beforeEach(() => {
    getMock.mockReset();
    getMock.mockResolvedValue({ unread_count: 0 });
  });

  it("renders the brand lockup linking to the Workspace List", () => {
    render(
      <AppShell displayName="Ada Lovelace" email="ada@example.com">
        <div>Screen content</div>
      </AppShell>,
    );

    const brand = screen.getByRole("link", { name: "Groundwork" });
    expect(brand).toHaveAttribute("href", "/workspaces");
    // The logo is decorative; the wordmark carries the accessible name.
    const logo = brand.querySelector("img");
    expect(logo).not.toBeNull();
    expect(logo!.getAttribute("alt")).toBe("");
    expect(logo!.className).toContain("dark:invert");
  });

  it("mounts the notifications bell and the account menu from the session identity", () => {
    render(
      <AppShell displayName="Ada Lovelace" email="ada@example.com">
        <div>Screen content</div>
      </AppShell>,
    );

    // The bell's accessible name comes from its unread state; it is present as a notifications link.
    expect(screen.getByRole("link", { name: "Notifications" })).toHaveAttribute(
      "href",
      "/notifications",
    );
    // Account menu is labelled with the signed-in email.
    expect(screen.getByRole("button", { name: "Account menu (ada@example.com)" })).toBeInTheDocument();
  });

  it("renders its children inside the shell", () => {
    render(
      <AppShell displayName="Ada" email="ada@example.com">
        <main id="main-content">Screen content</main>
      </AppShell>,
    );

    expect(screen.getByText("Screen content")).toBeInTheDocument();
    // Each screen keeps its own #main-content skip-link target; the shell must not duplicate it.
    expect(screen.getAllByRole("main", { name: "" })).toHaveLength(1);
  });

  it("keeps the bell's unread-count fetch local to the bell even when it fails", () => {
    getMock.mockRejectedValueOnce(new Error("offline"));
    render(
      <AppShell displayName="Ada" email="ada@example.com">
        <div>Screen content</div>
      </AppShell>,
    );

    // The bell swallows its own failures (NotificationsBell's contract); the shell itself renders.
    expect(screen.getByRole("link", { name: "Notifications" })).toBeInTheDocument();
  });

  it("renders the workspace switcher and sidebar within a workspace route", async () => {
    pathnameMock.mockReturnValue("/workspaces/ws-1");
    getMock.mockResolvedValue({ unread_count: 0, workspaces: listResponse().workspaces });
    render(
      <AppShell displayName="Ada" email="ada@example.com">
        <div>Screen content</div>
      </AppShell>,
    );

    // The switcher shows the current workspace name; the sidebar carries the three workspace links.
    expect(await screen.findByText("Q3 Numbers")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Home" })).toHaveAttribute(
      "href",
      "/workspaces/ws-1",
    );
    expect(screen.getByRole("link", { name: "Semantic Layer" })).toHaveAttribute(
      "href",
      "/workspaces/ws-1/semantic-layer",
    );
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute(
      "href",
      "/workspaces/ws-1/settings",
    );
  });

  it("opens the mobile drawer via the hamburger and closes it on Escape", async () => {
    pathnameMock.mockReturnValue("/workspaces/ws-1");
    getMock.mockResolvedValue({ unread_count: 0, workspaces: listResponse().workspaces });
    const user = userEvent.setup();
    render(
      <AppShell displayName="Ada" email="ada@example.com">
        <div>Screen content</div>
      </AppShell>,
    );

    await user.click(screen.getByRole("button", { name: "Open navigation menu" }));
    const dialog = screen.getByRole("dialog", { name: "Navigation menu" });
    expect(dialog).toBeInTheDocument();
    // The drawer carries the workspace switcher and the workspace links.
    expect(await within(dialog).findByText("Q3 Numbers")).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "Home" })).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Navigation menu" })).not.toBeInTheDocument(),
    );
  });

  it("closes the drawer after choosing a link inside it", async () => {
    pathnameMock.mockReturnValue("/workspaces/ws-1");
    getMock.mockResolvedValue({ unread_count: 0, workspaces: listResponse().workspaces });
    const user = userEvent.setup();
    render(
      <AppShell displayName="Ada" email="ada@example.com">
        <div>Screen content</div>
      </AppShell>,
    );

    await user.click(screen.getByRole("button", { name: "Open navigation menu" }));
    const dialog = screen.getByRole("dialog", { name: "Navigation menu" });
    await user.click(within(dialog).getByRole("link", { name: "Semantic Layer" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Navigation menu" })).not.toBeInTheDocument(),
    );
  });
});
