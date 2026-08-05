import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IngestProgressScreen } from "./IngestProgressScreen";
import { api, subscribeToStream } from "@/lib/api-client";
import type { IngestStreamEvent } from "./types";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

vi.mock("@/lib/api-client", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, post: vi.fn() },
    subscribeToStream: vi.fn(),
  };
});

const postMock = vi.mocked(api.post);
const subscribeMock = vi.mocked(subscribeToStream);

/** Captures the handlers passed to subscribeToStream so a test can push events into them. */
function captureStreamHandlers() {
  let handlers: Parameters<typeof subscribeToStream>[1] | undefined;
  subscribeMock.mockImplementation((_path, h) => {
    handlers = h;
    return vi.fn();
  });
  return {
    emit: (event: IngestStreamEvent) => handlers?.onMessage(JSON.stringify(event)),
    handlersRef: () => handlers,
  };
}

describe("IngestProgressScreen", () => {
  beforeEach(() => {
    pushMock.mockClear();
    postMock.mockReset();
    postMock.mockResolvedValue(undefined);
    subscribeMock.mockReset();
  });

  it("starts ingest on mount and opens the live status stream", () => {
    const { emit } = captureStreamHandlers();
    render(<IngestProgressScreen workspaceId="ws-1" />);

    expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/ingest");
    expect(subscribeMock).toHaveBeenCalledWith(
      "/v1/workspaces/ws-1/ingest/stream",
      expect.any(Object),
    );
    // Silence "not wrapped in act" noise from the unused emit reference in this test.
    void emit;
  });

  it("shows the Provisioning step active before any stream event arrives", () => {
    captureStreamHandlers();
    render(<IngestProgressScreen workspaceId="ws-1" />);

    expect(screen.getByText("Getting your data ready")).toBeInTheDocument();
    expect(screen.getByText("Provisioning engine")).toBeInTheDocument();
  });

  it("shows Loading data as active once the stream reports it", async () => {
    const { emit } = captureStreamHandlers();
    render(<IngestProgressScreen workspaceId="ws-1" />);

    emit({
      datasets: [{ id: "ds-1", filename: "orders.csv", step: "loading" }],
      overall_step: "loading",
      done: false,
    });

    await waitFor(() => {
      const loadingRow = screen.getByText("Loading data").closest("li");
      expect(loadingRow).toHaveTextContent("(in progress)");
    });
  });

  it("shows the Finding relationships step only when 2+ files are in the batch", async () => {
    const { emit } = captureStreamHandlers();
    render(<IngestProgressScreen workspaceId="ws-1" />);

    emit({
      datasets: [{ id: "ds-1", filename: "orders.csv", step: "provisioning" }],
      overall_step: "provisioning",
      done: false,
    });
    await waitFor(() => {
      expect(screen.queryByText("Finding relationships")).not.toBeInTheDocument();
    });

    emit({
      datasets: [
        { id: "ds-1", filename: "orders.csv", step: "provisioning" },
        { id: "ds-2", filename: "customers.csv", step: "provisioning" },
      ],
      overall_step: "provisioning",
      done: false,
    });
    await waitFor(() => {
      expect(screen.getByText("Finding relationships")).toBeInTheDocument();
    });
  });

  it("shows a specific, actionable error and a Back to Upload action on failure", async () => {
    const { emit } = captureStreamHandlers();
    render(<IngestProgressScreen workspaceId="ws-1" />);

    emit({
      datasets: [
        {
          id: "ds-1",
          filename: "bad.csv",
          step: "failed",
          error: "Could not read this CSV file: invalid unicode on line 2.",
        },
      ],
      overall_step: "failed",
      done: true,
    });

    await waitFor(() => {
      expect(screen.getByText("Ingest failed")).toBeInTheDocument();
    });
    expect(screen.getByText(/invalid unicode on line 2/)).toBeInTheDocument();
    expect(screen.getByText("bad.csv:")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back to Upload" })).toBeInTheDocument();
    // Must not auto-navigate to Ingest Confirmation on failure.
    expect(pushMock).not.toHaveBeenCalledWith("/workspaces/ws-1/ingest/confirm");
  });

  it("navigates to Ingest Confirmation once the stream reports done and not failed", async () => {
    const { emit } = captureStreamHandlers();
    render(<IngestProgressScreen workspaceId="ws-1" />);

    emit({
      datasets: [{ id: "ds-1", filename: "orders.csv", step: "profiling" }],
      overall_step: "profiling",
      done: true,
    });

    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith("/workspaces/ws-1/ingest/confirm"),
    );
  });

  it("shows a reconnecting notice, without failing the screen, on a stream error", async () => {
    let handlers: Parameters<typeof subscribeToStream>[1] | undefined;
    subscribeMock.mockImplementation((_path, h) => {
      handlers = h;
      return vi.fn();
    });
    render(<IngestProgressScreen workspaceId="ws-1" />);

    handlers?.onError?.(new Event("error"));

    await waitFor(() => {
      expect(screen.getByText(/Reconnecting/)).toBeInTheDocument();
    });
    // Still not the failure panel — a transport hiccup is not an ingest failure.
    expect(screen.queryByText("Ingest failed")).not.toBeInTheDocument();
  });
});
