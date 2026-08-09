import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { WorkspaceSidebar } from "./WorkspaceSidebar";

/**
 * WorkspaceSidebar reads the current path via next/navigation's usePathname. The mock router is
 * not needed (the component only reads, never pushes), so each test sets the pathname it wants.
 */
const pathnameMock = vi.fn(() => "/workspaces/ws-1");
vi.mock("next/navigation", () => ({
  usePathname: () => pathnameMock(),
}));

describe("WorkspaceSidebar", () => {
  it("renders the three workspace entries with correct links", () => {
    render(<WorkspaceSidebar workspaceId="ws-1" />);

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

  it("marks Home active on the workspace home path", () => {
    pathnameMock.mockReturnValue("/workspaces/ws-1");
    render(<WorkspaceSidebar workspaceId="ws-1" />);

    expect(screen.getByRole("link", { name: "Home" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Semantic Layer" })).not.toHaveAttribute(
      "aria-current",
    );
    expect(screen.getByRole("link", { name: "Settings" })).not.toHaveAttribute("aria-current");
  });

  it("marks Semantic Layer active on any semantic-layer subpage", () => {
    pathnameMock.mockReturnValue("/workspaces/ws-1/semantic-layer");
    render(<WorkspaceSidebar workspaceId="ws-1" />);

    expect(screen.getByRole("link", { name: "Semantic Layer" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: "Home" })).not.toHaveAttribute("aria-current");
  });

  it("marks Settings active on workspace settings", () => {
    pathnameMock.mockReturnValue("/workspaces/ws-1/settings");
    render(<WorkspaceSidebar workspaceId="ws-1" />);

    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
  });

  it("collapses to icon-only links and expands back", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSidebar workspaceId="ws-1" />);

    // Expanded: labels are visible.
    expect(screen.getByRole("link", { name: "Semantic Layer" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Collapse sidebar" }));
    // Collapsed: labels become titles, so the accessible name survives via the title attribute.
    expect(screen.getByRole("link", { name: "Semantic Layer" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Expand sidebar" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Expand sidebar" }));
    expect(screen.getByRole("button", { name: "Collapse sidebar" })).toBeInTheDocument();
  });
});
