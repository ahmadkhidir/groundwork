import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DatasetsPanel } from "./DatasetsPanel";
import { api } from "@/lib/api-client";

vi.mock("@/lib/api-client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), delete: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const deleteMock = vi.mocked(api.delete);

const DATASET = {
  id: "ds-1",
  original_filename: "customers.csv",
  ingest_status: "complete",
  row_count: 1200,
  created_at: "2026-08-05T14:00:00Z",
};

describe("DatasetsPanel", () => {
  beforeEach(() => {
    getMock.mockReset();
    deleteMock.mockReset();
  });

  afterEach(() => {
    vi.clearAllTimers();
  });

  it("lists uploaded datasets with filename, status, and row count", async () => {
    getMock.mockResolvedValueOnce({ datasets: [DATASET] });
    render(<DatasetsPanel workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("customers.csv")).toBeInTheDocument());
    expect(screen.getByText("complete")).toBeInTheDocument();
    expect(screen.getByText(/1,200 rows/)).toBeInTheDocument();
  });

  it("shows the empty state when the workspace has no datasets", async () => {
    getMock.mockResolvedValueOnce({ datasets: [] });
    render(<DatasetsPanel workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("No datasets yet")).toBeInTheDocument());
  });

  // FEAT-048's core non-negotiable: the confirmation must name a dependent Relationship AND, as a
  // SEPARATE warning, that type/PII review work is lost and not carried forward — never a bare
  // "are you sure?".
  it("names dependent relationships and warns about lost review work before delete can be confirmed", async () => {
    getMock.mockResolvedValueOnce({ datasets: [DATASET] });
    getMock.mockResolvedValueOnce({
      dataset: DATASET,
      column_count: 5,
      dependent_relationships: [
        { id: "rel-1", source_column_name: "customer_id", target_column_name: "customer_id" },
      ],
      has_reviewed_pii: true,
      has_type_override_risk: true,
    });

    const user = userEvent.setup();
    render(<DatasetsPanel workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("customers.csv")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /delete customers.csv/i }));

    await waitFor(() =>
      expect(screen.getByRole("dialog")).toHaveTextContent(/customer_id ↔ customer_id/),
    );
    expect(screen.getByRole("dialog")).toHaveTextContent(/permanently lost/i);
    expect(screen.getByRole("dialog")).toHaveTextContent(/NOT be carried forward/i);
  });

  it("removes the row and calls onDatasetDeleted after a confirmed delete", async () => {
    getMock.mockResolvedValueOnce({ datasets: [DATASET] });
    getMock.mockResolvedValueOnce({
      dataset: DATASET,
      column_count: 2,
      dependent_relationships: [],
      has_reviewed_pii: false,
      has_type_override_risk: false,
    });
    deleteMock.mockResolvedValueOnce({ id: "ds-1" });

    const onDeleted = vi.fn();
    const user = userEvent.setup();
    render(<DatasetsPanel workspaceId="ws-1" onDatasetDeleted={onDeleted} />);

    await waitFor(() => expect(screen.getByText("customers.csv")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /delete customers.csv/i }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(screen.queryByText("customers.csv")).not.toBeInTheDocument());
    expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/datasets/ds-1");
    expect(onDeleted).toHaveBeenCalledWith(0);
  });

  it("keeps the modal open and shows an inline error when the delete request fails", async () => {
    getMock.mockResolvedValueOnce({ datasets: [DATASET] });
    getMock.mockResolvedValueOnce({
      dataset: DATASET,
      column_count: 2,
      dependent_relationships: [],
      has_reviewed_pii: false,
      has_type_override_risk: false,
    });
    deleteMock.mockRejectedValueOnce(new Error("Could not delete this dataset. Try again."));

    const user = userEvent.setup();
    render(<DatasetsPanel workspaceId="ws-1" />);

    await waitFor(() => expect(screen.getByText("customers.csv")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /delete customers.csv/i }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    // The dataset must still be listed — the delete did not silently succeed.
    expect(screen.getByText("customers.csv")).toBeInTheDocument();
  });

  it("hides the per-row delete action for read-only access", async () => {
    getMock.mockResolvedValueOnce({ datasets: [DATASET] });
    render(<DatasetsPanel workspaceId="ws-1" readOnly />);

    await waitFor(() => expect(screen.getByText("customers.csv")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /delete customers.csv/i })).not.toBeInTheDocument();
  });
});
