import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DeletedProofScreen, deletionProofStorageKey } from "./DeletedProofScreen";
import { ApiError, api } from "@/lib/api-client";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

const pushMock = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

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

describe("DeletedProofScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    pushMock.mockReset();
  });

  afterEach(() => {
    sessionStorage.clear();
  });

  it("renders the itemized proof from the DELETE response stored in sessionStorage, without refetching", async () => {
    sessionStorage.setItem(
      deletionProofStorageKey("ws-1"),
      JSON.stringify(PROOF),
    );
    render(<DeletedProofScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Workspace deleted" }),
      ).toBeInTheDocument(),
    );

    // screens/14: itemized checkmarks per category in canonical order.
    const items = screen.getAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual([
      "Object store contents",
      "Semantic layer",
      "Reports and conversation history",
      "Query caches",
      "Logs",
    ]);

    // Locale-independent: toLocaleString() formatting varies by machine, but the year and the
    // "Completed" prefix are stable.
    expect(screen.getByText(/^Completed .*2026/)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Back to Workspaces" }),
    ).toHaveAttribute("href", "/workspaces");

    // The stored proof is the source of truth — no follow-up GET is made.
    expect(getMock).not.toHaveBeenCalled();
  });

  it("recovers the proof via the follow-up GET when sessionStorage is empty (refresh, lost storage)", async () => {
    getMock.mockResolvedValueOnce({ deletion: PROOF });
    render(<DeletedProofScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Workspace deleted" }),
      ).toBeInTheDocument(),
    );
    expect(getMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/deletion");
    expect(screen.getAllByRole("listitem")).toHaveLength(5);
  });

  it("shows an honest not-found state when the workspace has no deletion record (404)", async () => {
    getMock.mockRejectedValueOnce(
      new ApiError(404, "No deletion record found.", "not_found"),
    );
    render(<DeletedProofScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "No deletion record found" }),
      ).toBeInTheDocument(),
    );
    // A proof is never fabricated: no checkmarks, no timestamp.
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
    expect(
      screen.getByRole("link", { name: "Back to Workspaces" }),
    ).toHaveAttribute("href", "/workspaces");
  });

  it("shows the shared Error State with retry when the proof fetch fails", async () => {
    getMock.mockRejectedValueOnce(new ApiError(0, "Could not reach Groundwork."));
    const user = userEvent.setup();
    render(<DeletedProofScreen workspaceId="ws-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Something went wrong" }),
      ).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce({ deletion: PROOF });
    await user.click(screen.getByRole("button", { name: "Try Again" }));
    await waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "Workspace deleted" }),
      ).toBeInTheDocument(),
    );
  });
});
