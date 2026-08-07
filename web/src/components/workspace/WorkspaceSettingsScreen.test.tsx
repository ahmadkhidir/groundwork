import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceSettingsScreen } from "./WorkspaceSettingsScreen";
import { ApiError, api } from "@/lib/api-client";
import { deletionProofStorageKey } from "./DeletedProofScreen";
import { SubscriptionTier } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), delete: vi.fn(), post: vi.fn(), patch: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const deleteMock = vi.mocked(api.delete);
const postMock = vi.mocked(api.post);

// A stable object, not a fresh literal per call: Next's real useRouter() returns a referentially
// stable router, and tests that assert on the mock's calls would otherwise accumulate.
const pushMock = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

const WORKSPACE = {
  id: "ws-1",
  name: "Q3 Numbers",
  owner_id: "user-1",
  engine_status: "sleeping",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
  has_datasets: true,
};

const PROOF = {
  id: "proof-1",
  workspace_id: "ws-1",
  initiated_by: "user-1",
  deleted_categories: [
    "object_store",
    "semantic_layer",
    "reports_and_conversation",
    "query_caches",
    "logs",
  ],
  completed_at: "2026-08-07T14:30:00Z",
};

/** Routes the GET mock by path so each real tab's fetch resolves with its own shape. */
function mockGetByPath() {
  getMock.mockImplementation((path: string) => {
    if (path === "/v1/workspaces/ws-1") {
      return Promise.resolve({ workspace: WORKSPACE });
    }
    if (path === "/v1/workspaces/ws-1/columns") {
      return Promise.resolve({ columns: [] });
    }
    if (path === "/v1/workspaces/ws-1/semantic-layer/metrics") {
      return Promise.resolve({ metric_definitions: [] });
    }
    if (path === "/v1/workspaces/ws-1/usage") {
      return Promise.resolve({
        usage: { records: [], totals: { engine_seconds: 0, storage_bytes: 0, model_tokens: 0 } },
      });
    }
    if (path === "/v1/workspaces/ws-1/members") {
      return Promise.resolve({ members: [] });
    }
    if (path === "/v1/workspaces/ws-1/report-shares") {
      return Promise.resolve({ shares: [] });
    }
    if (path.startsWith("/v1/workspaces/ws-1/reports?")) {
      return Promise.resolve({ reports: [] });
    }
    return Promise.resolve({});
  });
}

describe("WorkspaceSettingsScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    deleteMock.mockReset();
    postMock.mockReset();
    pushMock.mockReset();
    mockGetByPath();
  });

  afterEach(() => {
    sessionStorage.clear();
  });

  it("renders the five settings tabs with the Audit Log tab active by default", async () => {
    render(<WorkspaceSettingsScreen workspaceId="ws-1" userTier={SubscriptionTier.Free} />);

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

  it("renders the Column Visibility tab with an honest empty state (FEAT-010)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    await user.click(screen.getByRole("tab", { name: "Column Visibility" }));

    expect(getMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/columns");
    await waitFor(() =>
      expect(screen.getByText("No columns yet")).toBeInTheDocument(),
    );
  });

  it("locks the Sharing sections behind the tier (FEAT-045/FEAT-046 upgrade prompts)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" userTier={SubscriptionTier.Free} />);

    await user.click(screen.getByRole("tab", { name: "Sharing" }));

    expect(screen.getByText(/Workspace sharing is available on the Team plan/)).toBeInTheDocument();
    expect(
      screen.getByText(/Shared report links are available on the Pro and Team plans/),
    ).toBeInTheDocument();
    // Locked sections never fetch their data.
    expect(getMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/members");
    expect(getMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/report-shares");
  });

  it("renders the Usage tab stat rows (FEAT-047)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" userTier={SubscriptionTier.Free} />);

    await user.click(screen.getByRole("tab", { name: "Usage" }));

    expect(getMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/usage");
    await waitFor(() => expect(screen.getByText("Engine time")).toBeInTheDocument());
    expect(screen.getByText("Storage")).toBeInTheDocument();
    expect(screen.getByText("Model tokens")).toBeInTheDocument();
  });

  it("a read-only shared member sees only the Usage tab and their access level (FEAT-045)", async () => {
    render(
      <WorkspaceSettingsScreen
        workspaceId="ws-1"
        userId="user-2"
        userTier={SubscriptionTier.Free}
      />,
    );

    await waitFor(() => expect(screen.getByText(/Read-only access/)).toBeInTheDocument());

    // Column Visibility, Sharing management, Audit Log, and Danger Zone are hidden entirely, not
    // just disabled (screens/13).
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual(["Usage"]);
    expect(screen.queryByRole("tab", { name: "Danger Zone" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Column Visibility" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Sharing" })).not.toBeInTheDocument();

    // The Usage tab is selected and rendered for the read-only member.
    expect(screen.getByRole("tab", { name: "Usage" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Engine time")).toBeInTheDocument();
  });

  it("keeps the back navigation to the workspace home", () => {
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);
    expect(screen.getByRole("link", { name: /Back to workspace/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1",
    );
  });

  it("loads the workspace name in the Danger Zone and opens the delete modal with explicit scope (FEAT-043)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    await user.click(screen.getByRole("tab", { name: "Danger Zone" }));

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Delete Workspace" }),
      ).toBeInTheDocument(),
    );
    expect(getMock).toHaveBeenCalledWith("/v1/workspaces/ws-1");

    await user.click(screen.getByRole("button", { name: "Delete Workspace" }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    // screens/14: the scope is explicit — never a bare "Are you sure?".
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Object store contents");
    expect(dialog).toHaveTextContent("Semantic layer");
    expect(dialog).toHaveTextContent("Reports and conversation history");
    expect(dialog).toHaveTextContent("Query caches");
    expect(dialog).toHaveTextContent("Logs");
  });

  it("requires typing the exact workspace name before Delete is enabled (Confirming → Ready to Confirm)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    await user.click(screen.getByRole("tab", { name: "Danger Zone" }));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Delete Workspace" }),
      ).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: "Delete Workspace" }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    const confirmButton = screen.getByTestId("confirm-workspace-delete");
    expect(confirmButton).toBeDisabled();

    // A case-sensitive typo stays rejected with an inline message, per screens/14's edge case.
    const input = screen.getByLabelText("Type the workspace name to confirm");
    await user.type(input, "q3 numbers");
    expect(confirmButton).toBeDisabled();
    expect(screen.getByText(/doesn't match Q3 Numbers/)).toBeInTheDocument();

    await user.clear(input);
    await user.type(input, "Q3 Numbers");
    expect(confirmButton).toBeEnabled();
  });

  it("executes the DELETE, carries the proof, and navigates to the Deletion Proof page", async () => {
    deleteMock.mockResolvedValueOnce({ deletion: PROOF });
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    await user.click(screen.getByRole("tab", { name: "Danger Zone" }));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Delete Workspace" }),
      ).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: "Delete Workspace" }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    await user.type(
      screen.getByLabelText("Type the workspace name to confirm"),
      "Q3 Numbers",
    );
    await user.click(screen.getByTestId("confirm-workspace-delete"));

    await waitFor(() =>
      expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1"),
    );
    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-1/deleted"),
    );
    expect(sessionStorage.getItem(deletionProofStorageKey("ws-1"))).toBe(
      JSON.stringify(PROOF),
    );
  });

  it("keeps the modal open and names the failed category on a partial cascade failure (Flow 5 Alternate Path B)", async () => {
    deleteMock.mockRejectedValueOnce(
      new ApiError(
        500,
        'The workspace was not deleted — the "semantic_layer" stage of the cascade failed. Your workspace is intact; retry the deletion.',
        "deletion_partial_failed",
      ),
    );
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    await user.click(screen.getByRole("tab", { name: "Danger Zone" }));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Delete Workspace" }),
      ).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: "Delete Workspace" }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    await user.type(
      screen.getByLabelText("Type the workspace name to confirm"),
      "Q3 Numbers",
    );
    await user.click(screen.getByTestId("confirm-workspace-delete"));

    // No false success: the dialog stays open, names the failed category, and never navigates.
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("semantic_layer"),
    );
    expect(pushMock).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // Retry is possible from the same dialog — the typed name still matches, so Delete is enabled.
    expect(screen.getByTestId("confirm-workspace-delete")).toBeEnabled();
  });

  it("canceling the modal makes no API call (Alternate Path A)", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSettingsScreen workspaceId="ws-1" />);

    await user.click(screen.getByRole("tab", { name: "Danger Zone" }));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Delete Workspace" }),
      ).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: "Delete Workspace" }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(deleteMock).not.toHaveBeenCalled();
  });
});
