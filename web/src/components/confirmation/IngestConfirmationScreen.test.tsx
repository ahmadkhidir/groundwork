import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IngestConfirmationScreen } from "./IngestConfirmationScreen";
import { ApiError, api } from "@/lib/api-client";
import type { ConfirmationCandidateJoin, ConfirmationColumn } from "./types";

const pushMock = vi.fn();
const routerMock = { push: pushMock };

vi.mock("next/navigation", () => ({
  useRouter: () => routerMock,
}));

vi.mock("@/lib/api-client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), patch: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const patchMock = vi.mocked(api.patch);

function column(overrides: Partial<ConfirmationColumn> = {}): ConfirmationColumn {
  return {
    id: "col-1",
    dataset_id: "ds-1",
    dataset_filename: "customers.csv",
    name: "customer_id",
    inferred_type: "integer",
    type_confidence: 0.92,
    pii_flag: false,
    pii_pattern: "",
    excluded_from_model: false,
    null_rate: 0,
    distinct_count: 3,
    ...overrides,
  };
}

function join(overrides: Partial<ConfirmationCandidateJoin> = {}): ConfirmationCandidateJoin {
  return {
    id: "cj-1",
    source_column_id: "col-1",
    target_column_id: "col-2",
    confidence_score: 0.81,
    basis: "matched column names; 90% sampled value overlap",
    status: "pending",
    created_at: "2026-08-05T00:00:00Z",
    ...overrides,
  };
}

function mockLoad(columns: ConfirmationColumn[], joins: ConfirmationCandidateJoin[] = []) {
  getMock.mockImplementation((path: string) => {
    if (path.includes("/candidate-joins")) {
      return Promise.resolve({ candidate_joins: joins });
    }
    return Promise.resolve({ columns });
  });
}

describe("IngestConfirmationScreen", () => {
  beforeEach(() => {
    pushMock.mockClear();
    getMock.mockReset();
    patchMock.mockReset();
  });

  it("shows a skeleton while the initial fetch is in flight", () => {
    getMock.mockReturnValue(new Promise(() => {}));
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    expect(screen.getByText("Loading what we found…")).toBeInTheDocument();
  });

  it("shows the shared error state with a working retry when the initial load fails", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<IngestConfirmationScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Something went wrong")).toBeInTheDocument());

    mockLoad([column()]);
    await userEvent.click(screen.getByRole("button", { name: "Try Again" }));

    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());
  });

  it("omits the Relationships step entirely for a single-file upload", async () => {
    mockLoad([column()]);
    render(<IngestConfirmationScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());
    expect(screen.queryByRole("tab", { name: /Relationships/ })).not.toBeInTheDocument();
  });

  it("shows the Relationships step for a 2+ file upload, based on distinct dataset count", async () => {
    mockLoad(
      [
        column({ id: "col-1", dataset_id: "ds-1" }),
        column({ id: "col-2", dataset_id: "ds-2", name: "customer_id" }),
      ],
      [],
    );
    render(<IngestConfirmationScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());
    expect(screen.getByRole("tab", { name: /Relationships/ })).toBeInTheDocument();
  });

  it("shows the empty state on the Relationships step when no candidates survived inference", async () => {
    mockLoad(
      [column({ id: "col-1", dataset_id: "ds-1" }), column({ id: "col-2", dataset_id: "ds-2" })],
      [],
    );
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("tab", { name: /Relationships/ }));
    expect(screen.getByText("No relationships found")).toBeInTheDocument();
  });

  // Every review step renders BOTH a mobile list and a desktop table (toggled purely via CSS media
  // queries, which jsdom does not evaluate), so any given row's text/controls legitimately appear
  // twice in the test DOM. Tests use getAllBy*()[0] throughout rather than getBy*, which is not a
  // sign of ambiguity in the real, rendered (single-viewport) UI.

  it("renders every profiled column's inferred type and confidence on the Types step", async () => {
    mockLoad([column({ name: "customer_id", inferred_type: "integer", type_confidence: 0.92 })]);
    render(<IngestConfirmationScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getAllByText("customer_id").length).toBeGreaterThan(0));
    expect(screen.getAllByText("92%").length).toBeGreaterThan(0);
  });

  it("overrides a column's type via PATCH and reflects the confirmed value", async () => {
    mockLoad([column({ id: "col-1", inferred_type: "integer" })]);
    patchMock.mockResolvedValue({
      column: column({ id: "col-1", inferred_type: "string", type_confidence: 1 }),
    });
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getAllByText("customer_id").length).toBeGreaterThan(0));

    const select = screen.getAllByLabelText("Override type for customer_id")[0];
    await userEvent.selectOptions(select, "string");

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/columns/col-1", {
        inferred_type: "string",
      }),
    );
  });

  it("shows a row-level error and preserves other rows when an override fails", async () => {
    mockLoad([
      column({ id: "col-1", name: "a" }),
      column({ id: "col-2", name: "b", dataset_id: "ds-1" }),
    ]);
    patchMock.mockRejectedValueOnce(new ApiError(500, "Could not save this change."));
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getAllByText("a").length).toBeGreaterThan(0));

    const select = screen.getAllByLabelText("Override type for a")[0];
    await userEvent.selectOptions(select, "string");

    await waitFor(() =>
      expect(screen.getAllByText("Could not save this change.").length).toBeGreaterThan(0),
    );
    // The other column's row is untouched.
    expect(screen.getAllByText("b").length).toBeGreaterThan(0);
  });

  it("flags a column as PII via the Sensitive Columns step", async () => {
    mockLoad([column({ id: "col-1", name: "notes", pii_flag: false })]);
    patchMock.mockResolvedValue({
      column: column({ id: "col-1", name: "notes", pii_flag: true, pii_pattern: "manual" }),
    });
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("tab", { name: /Sensitive columns/ }));
    await userEvent.click(screen.getAllByRole("button", { name: "Flag as sensitive" })[0]);

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/columns/col-1", { pii_flag: true }),
    );
  });

  it("accepts a candidate join and shows it as accepted", async () => {
    mockLoad(
      [column({ id: "col-1", dataset_id: "ds-1" }), column({ id: "col-2", dataset_id: "ds-2" })],
      [join({ id: "cj-1", status: "pending" })],
    );
    patchMock.mockResolvedValue({ candidate_join: join({ id: "cj-1", status: "accepted" }) });
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("tab", { name: /Relationships/ }));
    await userEvent.click(screen.getAllByRole("button", { name: /Accept/ })[0]);

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/candidate-joins/cj-1", {
        status: "accepted",
      }),
    );
    await waitFor(() => expect(screen.getAllByText("accepted").length).toBeGreaterThan(0));
  });

  it("rejects a candidate join without offering a 'use suggested defaults' shortcut on that step", async () => {
    mockLoad(
      [column({ id: "col-1", dataset_id: "ds-1" }), column({ id: "col-2", dataset_id: "ds-2" })],
      [join({ id: "cj-1", status: "pending" })],
    );
    patchMock.mockResolvedValue({ candidate_join: join({ id: "cj-1", status: "rejected" }) });
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("tab", { name: /Relationships/ }));
    expect(screen.queryByRole("button", { name: "Use suggested defaults" })).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: /Reject/ })[0]);
    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/candidate-joins/cj-1", {
        status: "rejected",
      }),
    );
  });

  it("marks a step 'defaults applied' without making any network call, and advances", async () => {
    mockLoad([column()]);
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: "Use suggested defaults" }));

    expect(patchMock).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByText(/defaults applied/)).toBeInTheDocument());
  });

  it("navigates to Workspace Home when Continue is pressed on the last step", async () => {
    mockLoad([column()]);
    render(<IngestConfirmationScreen workspaceId="ws-1" />);
    await waitFor(() => expect(screen.getByText("Confirm your data")).toBeInTheDocument());

    // Single-file upload: only Types and Sensitive columns steps exist, so Types -> Next -> last step.
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-1");
  });
});
