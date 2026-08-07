import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { WorkspaceSettingsScreen } from "./WorkspaceSettingsScreen";

describe("WorkspaceSettingsScreen", () => {
  it("renders the five settings tabs with the Audit Log tab active by default", () => {
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Workspace Settings" }),
    ).toBeInTheDocument();

    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "Column Visibility",
      "Sharing",
      "Usage",
      "Audit Log",
      "Danger Zone",
    ]);

    expect(screen.getByRole("tab", { name: "Audit Log" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("wires the Audit Log tab to the full audit-log screen (FEAT-042)", () => {
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    expect(screen.getByText(/Every model-egress event/)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "View the full audit log" }),
    ).toHaveAttribute("href", "/workspaces/ws-1/audit-log");
  });

  it("switches tabs and shows later-phase states, with no fake controls", () => {
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    fireEvent.click(screen.getByRole("tab", { name: "Usage" }));
    expect(screen.getByRole("tab", { name: "Usage" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/FEAT-047/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Column Visibility" }));
    expect(screen.getByText(/FEAT-010/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Sharing" }));
    expect(screen.getByText(/FEAT-045, FEAT-046/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Danger Zone" }));
    expect(screen.getByText(/FEAT-043/)).toBeInTheDocument();

    // No deletion or management button exists anywhere in the shell.
    expect(screen.queryByRole("button", { name: /Delete/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Remove/ })).not.toBeInTheDocument();
  });

  it("keeps the back navigation to the workspace home", () => {
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);
    expect(screen.getByRole("link", { name: /Back to workspace/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1",
    );
  });
});