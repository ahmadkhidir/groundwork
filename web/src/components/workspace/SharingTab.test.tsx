import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SharingTab } from "./SharingTab";
import { ApiError, api } from "@/lib/api-client";
import { SubscriptionTier } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), post: vi.fn(), delete: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);
const deleteMock = vi.mocked(api.delete);

function mockGetByPath({
  members = [],
  shares = [],
  reports = [],
}: {
  members?: unknown[];
  shares?: unknown[];
  reports?: unknown[];
} = {}) {
  getMock.mockImplementation((path: string) => {
    if (path === "/v1/workspaces/ws-1/members") {
      return Promise.resolve({ members });
    }
    if (path === "/v1/workspaces/ws-1/report-shares") {
      return Promise.resolve({ shares });
    }
    if (path.startsWith("/v1/workspaces/ws-1/reports?")) {
      return Promise.resolve({ reports });
    }
    return Promise.resolve({});
  });
}

describe("SharingTab", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
    deleteMock.mockReset();
  });

  it("locks both sections behind the tier with upgrade prompts on Free (screens/13 Locked state)", async () => {
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Free} />);

    expect(screen.getByText(/Workspace sharing is available on the Team plan/)).toBeInTheDocument();
    expect(
      screen.getByText(/Shared report links are available on the Pro and Team plans/),
    ).toBeInTheDocument();
    // Both locked sections carry the shared Upgrade Prompt, routed to the account plan screen.
    const upgradeLinks = screen.getAllByRole("link", { name: /Upgrade to Pro\/Team/ });
    expect(upgradeLinks).toHaveLength(2);
    for (const link of upgradeLinks) {
      expect(link).toHaveAttribute("href", "/settings");
    }
    // Locked sections never fetch (screens/13 Data Flow step 3).
    expect(getMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/members");
    expect(getMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/report-shares");
  });

  it("locks only the workspace-members section on Pro (FEAT-045 Team-only)", () => {
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Pro} />);

    expect(screen.getByText(/Workspace sharing is available on the Team plan/)).toBeInTheDocument();
    const upgradeLink = screen.getByRole("link", { name: /Upgrade to Pro\/Team/ });
    expect(upgradeLink).toHaveAttribute("href", "/settings");
    // Pro unlocks the report links section — it fetches.
    expect(getMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/report-shares");
    expect(getMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/members");
  });

  it("invites a member by email and refreshes the list (FEAT-045 AC1)", async () => {
    mockGetByPath();
    postMock.mockResolvedValue({ member: { id: "m-1" } });
    const user = userEvent.setup();
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Team} />);

    await waitFor(() =>
      expect(screen.getByText("No members yet")).toBeInTheDocument(),
    );

    await user.type(screen.getByLabelText("Invite by email"), "ngozi@example.com");
    await user.click(screen.getByRole("button", { name: "Invite" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/members", {
        email: "ngozi@example.com",
      }),
    );
  });

  it("lists members read-only and revokes one (FEAT-045 AC3)", async () => {
    mockGetByPath({
      members: [
        {
          id: "m-1",
          workspace_id: "ws-1",
          user_id: "user-2",
          email: "ngozi@example.com",
          display_name: "Ngozi",
          role: "read_only",
          invited_at: "2026-08-01T00:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Team} />);

    await waitFor(() => expect(screen.getByText("Ngozi")).toBeInTheDocument());
    expect(screen.getByText("read-only")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/members/m-1"),
    );
  });

  it("surfaces an invite error inline (e.g. no account for that email)", async () => {
    mockGetByPath();
    postMock.mockRejectedValue(
      new ApiError(404, "No account was found for that email. The person you invite must have a Groundwork account.", "invitee_not_found"),
    );
    const user = userEvent.setup();
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Team} />);

    await user.type(screen.getByLabelText("Invite by email"), "nobody@example.com");
    await user.click(screen.getByRole("button", { name: "Invite" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(/No account was found for that email/),
    );
  });

  it("creates a share link for a selected report (FEAT-046 AC1)", async () => {
    mockGetByPath({
      reports: [{ id: "report-1", title: "Revenue by region", created_at: "2026-08-01T00:00:00Z" }],
    });
    postMock.mockResolvedValue({ share: { id: "s-1", report_id: "report-1", share_token: "token-1" } });
    const user = userEvent.setup();
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Pro} />);

    await waitFor(() =>
      expect(screen.getByText("No active share links")).toBeInTheDocument(),
    );

    await user.selectOptions(screen.getByLabelText("Share a report"), "report-1");
    await user.click(screen.getByRole("button", { name: "Create link" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/reports/report-1/shares"),
    );
  });

  it("lists active share links with revoke (FEAT-046 AC3)", async () => {
    mockGetByPath({
      shares: [
        {
          id: "s-1",
          report_id: "report-1",
          share_token: "token-1",
          created_at: "2026-08-01T00:00:00Z",
        },
      ],
    });
    const user = userEvent.setup();
    render(<SharingTab workspaceId="ws-1" tier={SubscriptionTier.Pro} />);

    await waitFor(() => expect(screen.getByText(/\/shared\/token-1/)).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() =>
      expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/report-shares/s-1"),
    );
  });
});
