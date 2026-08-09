import { render, screen } from "@testing-library/react";
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

/**
 * The Navigation Shell (screens.md): the sticky Navbar shown on every authenticated screen —
 * brand lockup, notifications bell, and the account menu. The workspace-level Sidebar and the
 * workspace switcher are the remaining unbuilt pieces; workspace screens keep their local
 * contextual nav until they land.
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
});
