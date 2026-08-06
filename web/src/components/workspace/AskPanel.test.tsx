import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AskPanel } from "./AskPanel";
import { ApiError, api } from "@/lib/api-client";
import { routes } from "@/lib/routes";

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

describe("AskPanel", () => {
  beforeEach(() => {
    postMock.mockReset();
    pushMock.mockReset();
  });

  it("submits the trimmed question and navigates to the run viewer", async () => {
    const user = userEvent.setup();
    postMock.mockResolvedValue({
      run: {
        id: "run-1",
        workspace_id: "ws-1",
        question_text: "How many customers?",
        status: "queued",
        created_at: "2026-01-01T00:00:00Z",
      },
    });

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
    expect(postMock).not.toHaveBeenCalled();
  });

  it("disables the control while submitting", async () => {
    const user = userEvent.setup();
    let resolvePost: (value: { run: unknown }) => void;
    postMock.mockReturnValue(
      new Promise((resolve) => {
        resolvePost = resolve;
      }),
    );

    render(<AskPanel workspaceId="ws-1" />);

    await user.type(screen.getByPlaceholderText("Ask a question about your data..."), "question");
    await user.click(screen.getByRole("button", { name: "Submit question" }));

    expect(screen.getByPlaceholderText("Ask a question about your data...")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Submit question" })).toBeDisabled();

    resolvePost!({
      run: {
        id: "run-1",
        workspace_id: "ws-1",
        question_text: "question",
        status: "queued",
        created_at: "2026-01-01T00:00:00Z",
      },
    });
    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith(routes.run("ws-1", "run-1")),
    );
  });

  it("surfaces the API error and lets the user retry", async () => {
    const user = userEvent.setup();
    postMock.mockRejectedValueOnce(new ApiError(400, "Ask a question first.", "invalid_question"));

    render(<AskPanel workspaceId="ws-1" />);

    await user.type(screen.getByPlaceholderText("Ask a question about your data..."), "question");
    await user.click(screen.getByRole("button", { name: "Submit question" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("Ask a question first."),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });
});
