import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AskPanel } from "./AskPanel";
import { ApiError, api } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { EngineStatus } from "@/types/schema";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, post: vi.fn() },
  };
});

const postMock = vi.mocked(api.post);

// A stable object, not a fresh literal per call: Next's real useRouter() returns a referentially
// stable router, and tests that assert on the mock's calls would otherwise accumulate.
const pushMock = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

const RUN = {
  run: {
    id: "run-1",
    workspace_id: "ws-1",
    question_text: "How many customers?",
    status: "queued",
    created_at: "2026-01-01T00:00:00Z",
  },
};

/**
 * Routes the mock's response by path: wake-on-intent POSTs to /engine/wake (returning the engine's
 * own status), the question POSTs to /runs. Without this, the wake fired by focusing the input would
 * consume the run-creation mock.
 */
function mockPostByPath(overrides: { wake?: () => unknown; run?: () => unknown } = {}) {
  postMock.mockImplementation((path: string) => {
    if (path.endsWith("/engine/wake")) {
      return Promise.resolve(overrides.wake?.() ?? { engine_status: EngineStatus.Ready });
    }
    return Promise.resolve(overrides.run?.() ?? RUN);
  });
}

describe("AskPanel", () => {
  beforeEach(() => {
    postMock.mockReset();
    pushMock.mockReset();
    mockPostByPath();
  });

  it("submits the trimmed question and navigates to the run viewer", async () => {
    const user = userEvent.setup();

    render(<AskPanel workspaceId="ws-1" />);

    const input = screen.getByPlaceholderText("Ask a question about your data...");
    await user.type(input, "  How many customers?  ");
    await user.click(screen.getByRole("button", { name: "Submit question" }));

    expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/runs", {
      question_text: "How many customers?",
    });
    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith(routes.run("ws-1", "run-1")),
    );
  });

  it("does not submit a blank or whitespace-only question", async () => {
    const user = userEvent.setup();
    render(<AskPanel workspaceId="ws-1" />);

    await user.type(screen.getByPlaceholderText("Ask a question about your data..."), "   ");
    expect(screen.getByRole("button", { name: "Submit question" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Submit question" }));
    expect(postMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/runs", expect.anything());
  });

  it("disables the control while submitting", async () => {
    const user = userEvent.setup();
    let resolvePost: (value: { run: unknown }) => void;
    postMock.mockImplementation((path: string) => {
      if (path.endsWith("/engine/wake")) {
        return Promise.resolve({ engine_status: EngineStatus.Ready });
      }
      return new Promise((resolve) => {
        resolvePost = resolve;
      });
    });

    render(<AskPanel workspaceId="ws-1" />);

    await user.type(screen.getByPlaceholderText("Ask a question about your data..."), "question");
    await user.click(screen.getByRole("button", { name: "Submit question" }));

    expect(screen.getByPlaceholderText("Ask a question about your data...")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Submit question" })).toBeDisabled();

    resolvePost!(RUN);
    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith(routes.run("ws-1", "run-1")),
    );
  });

  it("surfaces the API error and lets the user retry", async () => {
    const user = userEvent.setup();
    postMock.mockImplementation((path: string) => {
      if (path.endsWith("/engine/wake")) {
        return Promise.resolve({ engine_status: EngineStatus.Ready });
      }
      return Promise.reject(new ApiError(400, "Ask a question first.", "invalid_question"));
    });

    render(<AskPanel workspaceId="ws-1" />);

    await user.type(screen.getByPlaceholderText("Ask a question about your data..."), "question");
    await user.click(screen.getByRole("button", { name: "Submit question" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("Ask a question first."),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });

  // ----------------------------------------------------------------------------------------------
  // FEAT-012 — wake-on-intent
  // ----------------------------------------------------------------------------------------------

  it("wakes a sleeping workspace when the input is focused (FEAT-012 AC1)", async () => {
    const user = userEvent.setup();
    const onStatus = vi.fn();
    render(
      <AskPanel
        workspaceId="ws-1"
        engineStatus={EngineStatus.Sleeping}
        onEngineStatusChange={onStatus}
      />,
    );

    await user.click(screen.getByPlaceholderText("Ask a question about your data..."));

    expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/engine/wake");
    await waitFor(() => expect(onStatus).toHaveBeenCalledWith(EngineStatus.Ready));
  });

  it("wakes on focus even when the parent has not wired engine status (standalone)", async () => {
    const user = userEvent.setup();
    render(<AskPanel workspaceId="ws-1" />);

    await user.click(screen.getByPlaceholderText("Ask a question about your data..."));

    expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/engine/wake");
  });

  it("does not wake when the engine is already ready", async () => {
    const user = userEvent.setup();
    render(<AskPanel workspaceId="ws-1" engineStatus={EngineStatus.Ready} />);

    await user.click(screen.getByPlaceholderText("Ask a question about your data..."));

    expect(postMock).not.toHaveBeenCalledWith("/v1/workspaces/ws-1/engine/wake");
  });

  it("does not re-fire the wake while one is already in flight", async () => {
    const user = userEvent.setup();
    let resolveWake: (value: { engine_status: EngineStatus }) => void;
    postMock.mockImplementation((path: string) => {
      if (path.endsWith("/engine/wake")) {
        return new Promise((resolve) => {
          resolveWake = resolve;
        });
      }
      return Promise.resolve(RUN);
    });

    render(<AskPanel workspaceId="ws-1" engineStatus={EngineStatus.Sleeping} />);

    const input = screen.getByPlaceholderText("Ask a question about your data...");
    await user.click(input);
    await user.click(input);
    await user.click(input);

    const wakeCalls = postMock.mock.calls.filter(([path]) => path.endsWith("/engine/wake"));
    expect(wakeCalls).toHaveLength(1);

    resolveWake!({ engine_status: EngineStatus.Ready });
  });

  it("an unused wake failure does not surface an error or break the input (FEAT-012 AC2)", async () => {
    const user = userEvent.setup();
    postMock.mockImplementation((path: string) => {
      if (path.endsWith("/engine/wake")) {
        return Promise.reject(new ApiError(503, "The engine is taking longer than usual to wake.", "engine_unavailable"));
      }
      return Promise.resolve(RUN);
    });

    render(<AskPanel workspaceId="ws-1" engineStatus={EngineStatus.Sleeping} />);

    await user.click(screen.getByPlaceholderText("Ask a question about your data..."));

    // No error surfaced for an unused wake — the input stays usable and the pill keeps whatever the
    // engine reports.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByPlaceholderText("Ask a question about your data...")).toBeEnabled();

    // A question still submits and wakes the engine on its own path.
    await user.type(screen.getByPlaceholderText("Ask a question about your data..."), "question");
    await user.click(screen.getByRole("button", { name: "Submit question" }));
    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith(routes.run("ws-1", "run-1")),
    );
  });
});
