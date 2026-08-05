import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuditLogScreen } from "./AuditLogScreen";
import { ApiError, api } from "@/lib/api-client";
import type { AuditLogEntryWithScope } from "./types";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return { ...actual, api: { ...actual.api, get: vi.fn() } };
});

const getMock = vi.mocked(api.get);

const workspaceEntry: AuditLogEntryWithScope = {
  id: "e1",
  workspace_id: "ws-1",
  user_id: "user-1",
  action_type: "query_executed",
  payload_sent_to_model: "",
  timestamp: "2026-01-01T00:00:00Z",
  scope: "workspace",
};

const modelCallEntry: AuditLogEntryWithScope = {
  id: "e2",
  workspace_id: "ws-1",
  user_id: "user-1",
  action_type: "model_call",
  payload_sent_to_model: '{"question":"revenue by region"}',
  timestamp: "2026-01-02T00:00:00Z",
  scope: "workspace",
};

const loginEntry: AuditLogEntryWithScope = {
  id: "e3",
  workspace_id: "",
  user_id: "user-1",
  action_type: "login",
  payload_sent_to_model: "",
  timestamp: "2026-01-03T00:00:00Z",
  scope: "account",
};

describe("AuditLogScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  it("shows the empty state for a brand-new workspace", async () => {
    getMock.mockResolvedValue({ entries: [], next_cursor: "" });
    render(<AuditLogScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(screen.getByText("No activity yet")).toBeInTheDocument(),
    );
  });

  it("shows the error state with a working retry", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "boom"));
    render(<AuditLogScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(screen.getByText("Something went wrong")).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce({ entries: [workspaceEntry], next_cursor: "" });
    await userEvent.click(screen.getByRole("button", { name: "Try Again" }));

    await waitFor(() => expect(screen.getAllByText("Query").length).toBeGreaterThan(0));
  });

  it("shows the account scope badge on a login entry, distinct from workspace entries", async () => {
    getMock.mockResolvedValue({ entries: [workspaceEntry, loginEntry], next_cursor: "" });
    render(<AuditLogScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getAllByText("Login").length).toBeGreaterThan(0));
    expect(screen.getAllByText("Account").length).toBeGreaterThan(0);
    expect(screen.getAllByText("This workspace").length).toBeGreaterThan(0);
  });

  it("expands a model_call entry to reveal the payload", async () => {
    getMock.mockResolvedValue({ entries: [modelCallEntry], next_cursor: "" });
    render(<AuditLogScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getAllByText("Model Call").length).toBeGreaterThan(0));

    // Index 0 is the "Model Call" FILTER CHIP; index 1 is the mobile card's action badge for this
    // entry — the one that actually toggles the detail expansion.
    const rows = screen.getAllByText("Model Call");
    await userEvent.click(rows[1]);

    await waitFor(() =>
      expect(screen.getAllByText(/revenue by region/).length).toBeGreaterThan(0),
    );
  });

  it("shows 'No model payload' for a non-model_call entry's detail", async () => {
    getMock.mockResolvedValue({ entries: [workspaceEntry], next_cursor: "" });
    render(<AuditLogScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getAllByText("Query").length).toBeGreaterThan(0));
    // Index 0 is the "Query" FILTER CHIP; index 1 is the mobile card's action badge.
    await userEvent.click(screen.getAllByText("Query")[1]);

    await waitFor(() =>
      expect(screen.getAllByText("No model payload for this action.").length).toBeGreaterThan(0),
    );
  });

  it("re-fetches from the first page when a filter changes", async () => {
    getMock.mockResolvedValue({ entries: [workspaceEntry], next_cursor: "" });
    render(<AuditLogScreen workspaceId="ws-1" />);
    await waitFor(() => expect(getMock).toHaveBeenCalledTimes(1));

    await userEvent.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() => expect(getMock).toHaveBeenCalledTimes(2));
    expect(getMock.mock.calls[1][0]).toContain("action_type=export");
  });

  it("shows filtered-empty distinctly from true-empty, with a clear-filters affordance", async () => {
    getMock.mockResolvedValue({ entries: [], next_cursor: "" });
    render(<AuditLogScreen workspaceId="ws-1" />);
    await userEvent.click(screen.getByRole("button", { name: "Deletion" }));

    await waitFor(() =>
      expect(screen.getByText("No matching activity")).toBeInTheDocument(),
    );
    expect(screen.getByText("Clear filters")).toBeInTheDocument();
  });

  it("appends the next page via Load more without resetting the existing list", async () => {
    getMock
      .mockResolvedValueOnce({ entries: [workspaceEntry], next_cursor: "cursor-1" })
      .mockResolvedValueOnce({ entries: [modelCallEntry], next_cursor: "" });

    render(<AuditLogScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getAllByText("Query").length).toBeGreaterThan(0));

    await userEvent.click(screen.getByRole("button", { name: "Load more" }));

    await waitFor(() => expect(screen.getAllByText("Model Call").length).toBeGreaterThan(0));
    // The first entry is still present.
    expect(screen.getAllByText("Query").length).toBeGreaterThan(0);
    // "Load more" disappears once next_cursor is empty.
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});
