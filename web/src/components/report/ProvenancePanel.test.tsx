import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProvenancePanel } from "./ProvenancePanel";
import { ApiError, api } from "@/lib/api-client";
import type { ProvenanceClaimDetail } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);

const { replaceMock, paramsHolder } = vi.hoisted(() => ({
  replaceMock: vi.fn(),
  paramsHolder: { value: "claim=claim-1" },
}));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: replaceMock }),
  useSearchParams: () => new URLSearchParams(paramsHolder.value),
}));

const DETAIL: ProvenanceClaimDetail = {
  claim: {
    id: "claim-1",
    report_id: "report-1",
    run_id: "run-1",
    claim_text: "10 customers in North",
    stated_value: "10",
    source_query: "SELECT region, count(*) FROM customers",
    source_column: "cnt",
    source_row_ids: ["0"],
  },
  source: {
    available: true,
    columns: ["region", "cnt"],
    rows: [["North", "10"]],
    highlight_column: "cnt",
  },
};

describe("ProvenancePanel", () => {
  beforeEach(() => {
    getMock.mockReset();
    replaceMock.mockReset();
    paramsHolder.value = "claim=claim-1";
  });

  it("renders nothing when no claim is in the URL", () => {
    getMock.mockResolvedValue({ detail: DETAIL });
    paramsHolder.value = "";
    const { container } = render(
      <ProvenancePanel workspaceId="ws-1" reportId="report-1" />,
    );
    expect(container.firstChild).toBeNull();
  });

  it("renders the stated figure, source SQL, and highlighted backing row once loaded", async () => {
    getMock.mockResolvedValue({ detail: DETAIL });
    render(<ProvenancePanel workspaceId="ws-1" reportId="report-1" />);

    await screen.findByRole("dialog", { name: "Provenance detail" });
    expect(screen.getByRole("heading", { level: 2, name: "10" })).toBeInTheDocument();
    expect(screen.getByLabelText("Source SQL")).toHaveTextContent(
      "SELECT region, count(*) FROM customers",
    );
    expect(screen.getByText("North")).toBeInTheDocument();
  });

  it("shows the source-unavailable state, distinct from an error", async () => {
    getMock.mockResolvedValue({
      detail: { claim: DETAIL.claim, source: { available: false } },
    });
    render(<ProvenancePanel workspaceId="ws-1" reportId="report-1" />);

    await screen.findByText("This source data is no longer available.");
    expect(screen.queryByRole("button", { name: "Try Again" })).not.toBeInTheDocument();
  });

  it("shows an inline error with retry when the fetch fails", async () => {
    getMock.mockRejectedValueOnce(new ApiError(500, "Server exploded"));
    render(<ProvenancePanel workspaceId="ws-1" reportId="report-1" />);

    await screen.findByText("Server exploded");
    expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument();
  });

  it("closing drops the claim param from the URL", async () => {
    getMock.mockResolvedValue({ detail: DETAIL });
    render(<ProvenancePanel workspaceId="ws-1" reportId="report-1" />);
    await screen.findByRole("dialog", { name: "Provenance detail" });

    screen.getByRole("button", { name: "Close provenance panel" }).click();
    expect(replaceMock).toHaveBeenCalledWith(expect.not.stringContaining("claim="));
  });
});