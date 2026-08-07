import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { NotificationsScreen } from "./NotificationsScreen";
import { ApiError, api } from "@/lib/api-client";
import { NotificationType } from "@/types/schema";
import type { NotificationWithLink } from "./types";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return { ...actual, api: { ...actual.api, get: vi.fn(), patch: vi.fn(), post: vi.fn() } };
});

const getMock = vi.mocked(api.get);
const patchMock = vi.mocked(api.patch);
const postMock = vi.mocked(api.post);

/** A notification whose timestamp is 5 minutes old, so the rendered relative time is stable ("5m ago"). */
function notification(overrides: Partial<NotificationWithLink> = {}): NotificationWithLink {
  return {
    id: "n1",
    user_id: "user-1",
    type: NotificationType.ExportReady,
    message: "Your report export is ready",
    related_entity_type: "report",
    related_entity_id: "report-1",
    read: false,
    created_at: new Date(Date.now() - 5 * 60_000).toISOString(),
    ...overrides,
  };
}

describe("NotificationsScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    patchMock.mockReset();
    postMock.mockReset();
  });

  it("shows skeleton rows while the list is being fetched", () => {
    getMock.mockReturnValue(new Promise(() => {}));
    render(<NotificationsScreen />);

    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
  });

  it("shows the empty state for a user with no notifications", async () => {
    getMock.mockResolvedValue({ notifications: [], unread_count: 0, next_cursor: "" });
    render(<NotificationsScreen />);

    await waitFor(() =>
      expect(screen.getByText("No notifications yet")).toBeInTheDocument(),
    );
  });

  it("distinguishes unread rows (accent + acknowledge action) from read history", async () => {
    getMock.mockResolvedValue({
      notifications: [
        notification({ id: "n-unread" }),
        notification({ id: "n-read", read: true }),
      ],
      unread_count: 1,
      next_cursor: "",
    });
    render(<NotificationsScreen />);

    await waitFor(() =>
      expect(screen.getAllByText("Your report export is ready").length).toBe(2),
    );

    // Unread row has an explicit acknowledge button; read history does not.
    expect(
      screen.getByRole("button", { name: 'Mark "Your report export is ready" as read' }),
    ).toBeInTheDocument();
    // Both rows carry their relative timestamp.
    expect(screen.getAllByText("5m ago").length).toBe(2);
    // The unread count drives the "Mark all as read" action.
    expect(screen.getByRole("button", { name: "Mark all as read" })).toBeInTheDocument();
  });

  it("acknowledges one notification via PATCH and refreshes the list", async () => {
    getMock
      .mockResolvedValueOnce({
        notifications: [notification()],
        unread_count: 1,
        next_cursor: "",
      })
      .mockResolvedValueOnce({
        notifications: [notification({ read: true })],
        unread_count: 0,
        next_cursor: "",
      });
    patchMock.mockResolvedValue({ ok: true });
    render(<NotificationsScreen />);

    await userEvent.click(
      await screen.findByRole("button", { name: 'Mark "Your report export is ready" as read' }),
    );

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith("/v1/notifications/n1", { read: true }),
    );
    // The list refreshed after acknowledge: the row is now read and the badge actions disappear.
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: 'Mark "Your report export is ready" as read' }),
      ).not.toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: "Mark all as read" }),
    ).not.toBeInTheDocument();
  });

  it("marks all as read via POST and refreshes the list", async () => {
    getMock
      .mockResolvedValueOnce({
        notifications: [notification({ workspace_id: "ws-1" })],
        unread_count: 1,
        next_cursor: "",
      })
      .mockResolvedValueOnce({
        notifications: [notification({ read: true, workspace_id: "ws-1" })],
        unread_count: 0,
        next_cursor: "",
      });
    postMock.mockResolvedValue({ marked_read: 2 });
    render(<NotificationsScreen />);

    await userEvent.click(await screen.findByRole("button", { name: "Mark all as read" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith("/v1/notifications/read-all"),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Mark all as read" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("shows the error state with a working retry", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "boom"));
    render(<NotificationsScreen />);

    await waitFor(() =>
      expect(screen.getByText("Something went wrong")).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce({
      notifications: [notification()],
      unread_count: 1,
      next_cursor: "",
    });
    await userEvent.click(screen.getByRole("button", { name: "Try Again" }));

    await waitFor(() =>
      expect(screen.getByText("Your report export is ready")).toBeInTheDocument(),
    );
  });

  it("shows the Pro/Team upgrade state on the monetization gate (403)", async () => {
    getMock.mockRejectedValue(
      new ApiError(403, "Durable notifications are available on the Pro and Team plans.", "upgrade_required"),
    );
    render(<NotificationsScreen />);

    await waitFor(() =>
      expect(screen.getByText("Notifications are a Pro and Team feature")).toBeInTheDocument(),
    );
    expect(screen.getByRole("link", { name: "Manage your plan" })).toBeInTheDocument();
  });

  it("deep-links each notification type to its documented screen", async () => {
    getMock.mockResolvedValue({
      notifications: [
        notification({
          id: "n-export",
          type: NotificationType.ExportReady,
          workspace_id: "ws-1",
          related_entity_id: "report-9",
          message: "Export finished",
        }),
        notification({
          id: "n-engine",
          type: NotificationType.EngineSlept,
          workspace_id: "ws-1",
          related_entity_id: "ws-1",
          message: "Engine went to sleep",
        }),
        notification({
          id: "n-ingest",
          type: NotificationType.IngestFailed,
          workspace_id: "ws-1",
          related_entity_id: "ds-1",
          message: "Ingest failed",
        }),
        notification({
          id: "n-invite",
          type: NotificationType.WorkspaceInvite,
          workspace_id: "ws-9",
          related_entity_id: "ws-9",
          message: "You were invited",
        }),
      ],
      unread_count: 4,
      next_cursor: "",
    });
    render(<NotificationsScreen />);

    await waitFor(() =>
      expect(screen.getByText("Export finished")).toBeInTheDocument(),
    );

    // screens/16 Exit Points: export ready → Report View, engine slept → workspace, ingest failed →
    // File Upload, workspace invite → Workspace List.
    expect(screen.getByRole("link", { name: /Export finished/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1/reports/report-9",
    );
    expect(screen.getByRole("link", { name: /Engine went to sleep/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1",
    );
    expect(screen.getByRole("link", { name: /Ingest failed/ })).toHaveAttribute(
      "href",
      "/workspaces/ws-1/upload",
    );
    expect(screen.getByRole("link", { name: /You were invited/ })).toHaveAttribute(
      "href",
      "/workspaces",
    );
  });

  it("disables the link with a tooltip when the linked entity was deleted", async () => {
    getMock.mockResolvedValue({
      notifications: [
        // No workspace_id: the server could not resolve the report — it no longer exists.
        notification({ id: "n-gone", message: "Export for deleted report" }),
      ],
      unread_count: 1,
      next_cursor: "",
    });
    render(<NotificationsScreen />);

    await waitFor(() =>
      expect(screen.getByText("Export for deleted report")).toBeInTheDocument(),
    );

    // The row is NOT a link — it is a historical record with an explanatory tooltip.
    expect(
      screen.queryByRole("link", { name: /Export for deleted report/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByTitle("This linked item is no longer available.")).toBeInTheDocument();
  });

  it("marks a notification read when its row is opened (auto-on-open) and still navigates", async () => {
    getMock
      .mockResolvedValueOnce({
        notifications: [notification({ workspace_id: "ws-1" })],
        unread_count: 1,
        next_cursor: "",
      })
      .mockResolvedValueOnce({
        notifications: [notification({ read: true, workspace_id: "ws-1" })],
        unread_count: 0,
        next_cursor: "",
      });
    patchMock.mockResolvedValue({ ok: true });
    render(<NotificationsScreen />);

    const rowLink = await screen.findByRole("link", { name: /Your report export is ready/ });
    await userEvent.click(rowLink);

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith("/v1/notifications/n1", { read: true }),
    );
  });

  it("appends the next page via Load more without resetting the existing list", async () => {
    getMock
      .mockResolvedValueOnce({
        notifications: [notification({ id: "n1", message: "First export" })],
        unread_count: 1,
        next_cursor: "cursor-1",
      })
      .mockResolvedValueOnce({
        notifications: [notification({ id: "n2", message: "Older export", read: true })],
        unread_count: 1,
        next_cursor: "",
      });
    render(<NotificationsScreen />);

    await waitFor(() => expect(screen.getByText("First export")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: "Load more" }));

    await waitFor(() => expect(screen.getByText("Older export")).toBeInTheDocument());
    expect(getMock.mock.calls[1][0]).toContain("cursor=cursor-1");
    // The first page is still present, and Load more disappears once history is exhausted.
    expect(screen.getByText("First export")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});
