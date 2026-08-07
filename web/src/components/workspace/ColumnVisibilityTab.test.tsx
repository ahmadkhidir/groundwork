import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ColumnVisibilityTab } from "./ColumnVisibilityTab";
import { ApiError, api } from "@/lib/api-client";
import type { ConfirmationColumn } from "@/components/confirmation/types";
import { SubscriptionTier, type InferredType } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), patch: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const patchMock = vi.mocked(api.patch);

/**
 * The tests that exercise the exclusion toggle render on a paid tier — FEAT-010 is Pro/Team
 * (monetization.md), so the toggle only exists there. The Free-tier locked state has its own test.
 */
const PAID_TIER = SubscriptionTier.Team;

function column(overrides: Partial<ConfirmationColumn> = {}): ConfirmationColumn {
  return {
    id: "col-1",
    dataset_id: "ds-1",
    dataset_filename: "orders.csv",
    dataset_created_at: "2026-08-01T10:00:00Z",
    name: "total",
    inferred_type: "number" as InferredType,
    type_confidence: 0.9,
    pii_flag: false,
    pii_pattern: "",
    excluded_from_model: false,
    null_rate: 0,
    distinct_count: 100,
    ...overrides,
  };
}

/** Routes the GET mock by path: columns, plus metrics for the exclusion warning. */
function mockGetByPath(columns: ConfirmationColumn[], metrics: unknown[] = []) {
  getMock.mockImplementation((path: string) => {
    if (path === "/v1/workspaces/ws-1/columns") {
      return Promise.resolve({ columns });
    }
    if (path === "/v1/workspaces/ws-1/semantic-layer/metrics") {
      return Promise.resolve({ metric_definitions: metrics });
    }
    return Promise.resolve({});
  });
}

/**
 * The tab renders both the desktop table and the mobile list (CSS-hidden per viewport, like
 * PIIReviewStep's dual views), so each column's toggle exists twice in the DOM. Tests target the
 * first match — the desktop table's.
 */
function switchFor(name: string): HTMLElement {
  return screen.getAllByRole("switch", { name })[0];
}

describe("ColumnVisibilityTab", () => {
  beforeEach(() => {
    getMock.mockReset();
    patchMock.mockReset();
  });

  it("renders a loading skeleton then the loaded column table grouped by dataset", async () => {
    mockGetByPath([
      column({ id: "col-1", name: "total", pii_flag: true, pii_pattern: "amount" }),
      column({ id: "col-2", name: "email", excluded_from_model: true }),
    ]);
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getAllByText("total").length).toBeGreaterThan(0));

    expect(screen.getAllByText(/orders\.csv/).length).toBeGreaterThan(0);
    expect(screen.getAllByText("email").length).toBeGreaterThan(0);
    // PII flag shown for context (screens/13).
    expect(screen.getAllByText("amount").length).toBeGreaterThan(0);
    // Exclusion state shown per column.
    expect(switchFor("Exclude email from model visibility")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(switchFor("Exclude total from model visibility")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("locks the exclusion toggle behind the Upgrade Prompt on Free (FEAT-010, screens.md), routed to the plan screen", async () => {
    mockGetByPath([column({ id: "col-1", name: "total" })]);
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={SubscriptionTier.Free} />);

    await waitFor(() => expect(screen.getAllByText("total").length).toBeGreaterThan(0));

    // The column and PII flag stay visible (PII flagging is on every tier); the exclusion control
    // is a locked Upgrade Prompt, not a switch the server would reject with a 403.
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    const upgradeLinks = screen.getAllByRole("link", { name: /Upgrade to Pro\/Team/ });
    expect(upgradeLinks.length).toBeGreaterThan(0);
    for (const link of upgradeLinks) {
      expect(link).toHaveAttribute("href", "/settings");
    }
    // Never a fetch of the exclusion route for a locked tier.
    expect(patchMock).not.toHaveBeenCalled();
  });

  it("shows an honest empty state when no columns exist", async () => {
    mockGetByPath([]);
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getByText("No columns yet")).toBeInTheDocument());
  });

  it("shows the shared error state with retry when the fetch fails", async () => {
    getMock.mockRejectedValue(new ApiError(500, "Something went wrong. Try again.", "internal_error"));
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument());
  });

  it("toggles exclusion optimistically and PATCHes the exclusion route (FEAT-010)", async () => {
    mockGetByPath([column({ id: "col-1", name: "salary" })]);
    patchMock.mockResolvedValue({
      column: column({ id: "col-1", name: "salary", excluded_from_model: true }),
    });
    const user = userEvent.setup();
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getAllByText("salary").length).toBeGreaterThan(0));

    const toggle = switchFor("Exclude salary from model visibility");
    expect(toggle).toHaveAttribute("aria-checked", "false");

    await user.click(toggle);

    expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/columns/col-1/exclusion", {
      excluded_from_model: true,
    });
    await waitFor(() =>
      expect(
        switchFor("Exclude salary from model visibility"),
      ).toHaveAttribute("aria-checked", "true"),
    );
  });

  it("rolls the toggle back with an inline error when the PATCH fails (screens/13)", async () => {
    mockGetByPath([column({ id: "col-1", name: "salary" })]);
    patchMock.mockRejectedValue(new ApiError(500, "Something went wrong. Try again.", "internal_error"));
    const user = userEvent.setup();
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getAllByText("salary").length).toBeGreaterThan(0));

    await user.click(switchFor("Exclude salary from model visibility"));

    await waitFor(() =>
      expect(switchFor("Exclude salary from model visibility")).toHaveAttribute(
        "aria-checked",
        "false",
      ),
    );
    expect(screen.getAllByText("Something went wrong. Try again.").length).toBeGreaterThan(0);
  });

  it("warns before excluding a column referenced by a metric definition (screens/13 edge case)", async () => {
    mockGetByPath([column({ id: "col-1", name: "total" })], [
      {
        id: "m-1",
        workspace_id: "ws-1",
        name: "revenue",
        sql_expression: "SUM(total)",
        description: "",
        created_at: "2026-08-01T00:00:00Z",
        updated_at: "2026-08-01T00:00:00Z",
      },
    ]);
    patchMock.mockResolvedValue({
      column: column({ id: "col-1", name: "total", excluded_from_model: true }),
    });
    const user = userEvent.setup();
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getAllByText("total").length).toBeGreaterThan(0));

    await user.click(switchFor("Exclude total from model visibility"));

    // The warning appears BEFORE the PATCH — never a silent block.
    expect(patchMock).not.toHaveBeenCalled();
    expect(screen.getByText(/used in 1 metric definition/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Exclude anyway" }));
    expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/columns/col-1/exclusion", {
      excluded_from_model: true,
    });
  });

  it("does not warn when the column is not referenced by any metric definition", async () => {
    mockGetByPath([column({ id: "col-1", name: "notes" })], [
      {
        id: "m-1",
        workspace_id: "ws-1",
        name: "revenue",
        sql_expression: "SUM(total)",
        description: "",
        created_at: "2026-08-01T00:00:00Z",
        updated_at: "2026-08-01T00:00:00Z",
      },
    ]);
    patchMock.mockResolvedValue({
      column: column({ id: "col-1", name: "notes", excluded_from_model: true }),
    });
    const user = userEvent.setup();
    render(<ColumnVisibilityTab workspaceId="ws-1" tier={PAID_TIER} />);

    await waitFor(() => expect(screen.getAllByText("notes").length).toBeGreaterThan(0));

    await user.click(switchFor("Exclude notes from model visibility"));

    expect(patchMock).toHaveBeenCalled();
    expect(screen.queryByText(/metric definition/)).not.toBeInTheDocument();
  });
});
