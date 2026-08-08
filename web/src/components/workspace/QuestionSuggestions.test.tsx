import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QuestionSuggestions } from "./QuestionSuggestions";
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

const SUGGESTIONS = [
  "What is the total total amount?",
  "What are the most common customer id values?",
  "How does total amount change over time?",
];

describe("QuestionSuggestions", () => {
  beforeEach(() => {
    getMock.mockReset();
  });

  it("shows skeleton chips while loading, then the suggestions as chips", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS });
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} />);

    expect(screen.getByLabelText("Loading suggested questions")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByText("What is the total total amount?")).toBeInTheDocument(),
    );
    expect(screen.getByText("Suggested questions")).toBeInTheDocument();
  });

  it("fills the Ask input with the clicked suggestion's exact text (never auto-submits)", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS });
    const onFill = vi.fn();
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={onFill} />);

    const chip = await screen.findByRole("button", { name: SUGGESTIONS[0] });
    await user.click(chip);

    expect(onFill).toHaveBeenCalledWith(SUGGESTIONS[0]);
    expect(onFill).toHaveBeenCalledTimes(1);
  });

  it("renders nothing when no suggestions are possible (never padded)", async () => {
    getMock.mockResolvedValue({ suggestions: [] });
    const { container } = render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} />);

    await waitFor(() => expect(screen.queryByText("Suggested questions")).not.toBeInTheDocument());
    // Nothing but the (empty) container remains.
    expect(container.firstChild).toBeNull();
  });

  it("shows the shared error state with retry on a failed fetch (incl. offline)", async () => {
    getMock.mockRejectedValueOnce(
      new ApiError(0, "Could not reach Groundwork. Check your connection and try again."),
    );
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce({ suggestions: SUGGESTIONS });
    await user.click(screen.getByRole("button", { name: "Try Again" }));
    await waitFor(() =>
      expect(screen.getByText("What is the total total amount?")).toBeInTheDocument(),
    );
  });
});
