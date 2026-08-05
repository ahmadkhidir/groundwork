import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SemanticLayerScreen } from "./SemanticLayerScreen";
import { ApiError, api } from "@/lib/api-client";
import type { SemanticCorrection, SemanticMetricDefinition, SemanticRelationship } from "./types";

vi.mock("@/lib/api-client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), patch: vi.fn(), post: vi.fn(), delete: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const patchMock = vi.mocked(api.patch);
const postMock = vi.mocked(api.post);
const deleteMock = vi.mocked(api.delete);

function relationship(overrides: Partial<SemanticRelationship> = {}): SemanticRelationship {
  return {
    id: "rel-1",
    workspace_id: "ws-1",
    source_column_id: "col-a",
    target_column_id: "col-b",
    cardinality: "one_to_many",
    grain_notes: "",
    confirmed_at: "2026-08-05T00:00:00Z",
    ...overrides,
  };
}

function metric(overrides: Partial<SemanticMetricDefinition> = {}): SemanticMetricDefinition {
  return {
    id: "metric-1",
    workspace_id: "ws-1",
    name: "revenue",
    sql_expression: "SUM(total)",
    description: "Gross revenue.",
    created_at: "2026-08-05T00:00:00Z",
    updated_at: "2026-08-05T00:00:00Z",
    ...overrides,
  };
}

function correction(overrides: Partial<SemanticCorrection> = {}): SemanticCorrection {
  return {
    id: "corr-1",
    workspace_id: "ws-1",
    description: "refunds join now excludes cancelled orders",
    original_logic: "JOIN refunds ON orders.id = refunds.order_id",
    corrected_logic: "JOIN refunds ON orders.id = refunds.order_id WHERE status != 'cancelled'",
    originating_run_id: "run-1",
    created_at: "2026-08-05T00:00:00Z",
    ...overrides,
  };
}

function mockLoad({
  relationships = [],
  columns = [],
  metrics = [],
  corrections = [],
}: {
  relationships?: SemanticRelationship[];
  columns?: unknown[];
  metrics?: SemanticMetricDefinition[];
  corrections?: SemanticCorrection[];
}) {
  getMock.mockImplementation((path: string) => {
    if (path.includes("/semantic-layer/relationships")) return Promise.resolve({ relationships });
    if (path.includes("/columns")) return Promise.resolve({ columns });
    if (path.includes("/semantic-layer/metrics")) return Promise.resolve({ metric_definitions: metrics });
    if (path.includes("/semantic-layer/corrections")) return Promise.resolve({ corrections });
    return Promise.reject(new Error(`unexpected path ${path}`));
  });
}

describe("SemanticLayerScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    patchMock.mockReset();
    postMock.mockReset();
    deleteMock.mockReset();
  });

  it("shows a skeleton while the Relationships tab's initial fetch is in flight", () => {
    getMock.mockReturnValue(new Promise(() => {}));
    render(<SemanticLayerScreen workspaceId="ws-1" />);
    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
  });

  it("shows the shared error state with a working retry when the Relationships tab fails to load", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<SemanticLayerScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("Something went wrong")).toBeInTheDocument());

    mockLoad({ relationships: [relationship()], columns: [] });
    await userEvent.click(screen.getByRole("button", { name: "Try Again" }));

    await waitFor(() => expect(screen.getAllByText(/col-a.*col-b/).length).toBeGreaterThan(0));
  });

  it("renders the Relationships tab's empty state for a new workspace", async () => {
    mockLoad({});
    render(<SemanticLayerScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("No relationships yet")).toBeInTheDocument());
  });

  it("labels relationship columns using the workspace's column list, and edits cardinality/grain notes", async () => {
    mockLoad({
      relationships: [relationship()],
      columns: [
        { id: "col-a", dataset_id: "ds-1", dataset_filename: "orders.csv", name: "customer_id" },
        { id: "col-b", dataset_id: "ds-2", dataset_filename: "customers.csv", name: "id" },
      ],
    });
    patchMock.mockResolvedValue({
      relationship: relationship({ cardinality: "one_to_one", grain_notes: "one row per customer" }),
    });

    render(<SemanticLayerScreen workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getAllByText(/orders.csv: customer_id/).length).toBeGreaterThan(0));
    expect(screen.getAllByText(/customers.csv: id/).length).toBeGreaterThan(0);

    // Both the mobile list and the web table render an Edit action for the same relationship;
    // click the first one (web table, since it's the first Edit button with the plain "Edit" name).
    const editButtons = screen.getAllByRole("button", { name: "Edit" });
    await userEvent.click(editButtons[0]);

    await userEvent.click(screen.getAllByRole("button", { name: "Save" })[0]);

    await waitFor(() =>
      expect(patchMock).toHaveBeenCalledWith(
        "/v1/workspaces/ws-1/semantic-layer/relationships/rel-1",
        expect.objectContaining({ cardinality: "one_to_many" }),
      ),
    );
  });

  it("renders the Metric Definitions tab's empty state and creates a new metric", async () => {
    mockLoad({});
    postMock.mockResolvedValue({ metric_definition: metric() });
    render(<SemanticLayerScreen workspaceId="ws-1" />);

    await userEvent.click(screen.getByRole("tab", { name: "Metric Definitions" }));
    await waitFor(() => expect(screen.getByText("No metric definitions yet")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: "New metric" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Name"), "revenue");
    await userEvent.type(within(dialog).getByLabelText("SQL expression"), "SUM(total)");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/v1/workspaces/ws-1/semantic-layer/metrics",
        expect.objectContaining({ name: "revenue", sql_expression: "SUM(total)" }),
      ),
    );
    await waitFor(() => expect(screen.getAllByText("revenue").length).toBeGreaterThan(0));
  });

  it("edits an existing metric definition and deletes another", async () => {
    mockLoad({ metrics: [metric(), metric({ id: "metric-2", name: "cost" })] });
    patchMock.mockResolvedValue({ metric_definition: metric({ description: "updated" }) });
    deleteMock.mockResolvedValue(undefined);

    render(<SemanticLayerScreen workspaceId="ws-1" />);
    await userEvent.click(screen.getByRole("tab", { name: "Metric Definitions" }));
    await waitFor(() => expect(screen.getAllByText("revenue").length).toBeGreaterThan(0));

    const editButtons = screen.getAllByRole("button", { name: /^edit/i });
    await userEvent.click(editButtons[0]);
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/semantic-layer/metrics/metric-1", expect.anything()));

    const deleteButtons = screen.getAllByRole("button", { name: /^delete/i });
    await userEvent.click(deleteButtons[0]);
    await waitFor(() => expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/semantic-layer/metrics/metric-1"));
  });

  it("renders the Corrections tab's empty state (legitimate until FEAT-020)", async () => {
    mockLoad({});
    render(<SemanticLayerScreen workspaceId="ws-1" />);

    await userEvent.click(screen.getByRole("tab", { name: "Corrections" }));
    await waitFor(() => expect(screen.getByText("No corrections yet")).toBeInTheDocument());
  });

  it("shows a working run link for a correction, and disables it when the originating run is gone", async () => {
    mockLoad({
      corrections: [
        correction({ id: "corr-with-run", originating_run_id: "run-1" }),
        correction({ id: "corr-no-run", originating_run_id: "" }),
      ],
    });
    render(<SemanticLayerScreen workspaceId="ws-1" />);

    await userEvent.click(screen.getByRole("tab", { name: "Corrections" }));
    await waitFor(() => expect(screen.getAllByText("View run").length).toBeGreaterThan(0));
    expect(screen.getAllByText("View run (unavailable)").length).toBeGreaterThan(0);

    const links = screen.getAllByRole("link", { name: "View run" });
    expect(links[0]).toHaveAttribute("href", "/workspaces/ws-1/runs/run-1");
  });
});
