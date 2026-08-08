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
    api: { ...actual.api, get: vi.fn(), post: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);

const SUGGESTIONS = [
  "What is the total total amount?",
  "What are the most common customer id values?",
  "How does total amount change over time?",
];

const SMART = [
  "Which region drove the highest total amount last quarter?",
  "What explains the recent peak in total amount?",
];

describe("QuestionSuggestions", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
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

  // ---- FEAT-051 smart generation -------------------------------------------------------------

  it("locks the smart affordance as the Upgrade Prompt on Free — no request, no wake", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS });
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="free" />);

    const upgrade = await screen.findByRole("link", { name: /Upgrade to Pro\/Team/ });
    expect(upgrade).toHaveAttribute("href", "/settings");
    expect(screen.queryByRole("button", { name: /Generate smarter questions/ })).not.toBeInTheDocument();
    expect(postMock).not.toHaveBeenCalled();
  });

  it("generates smart suggestions on Pro/Team and swaps the chips for the session with a revert", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS });
    postMock.mockResolvedValue({ suggestions: SMART, model_generated: true });
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="pro" />);

    await screen.findByText("What is the total total amount?");
    await user.click(screen.getByRole("button", { name: /Generate smarter questions/ }));

    expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/suggestions/smart");
    // The model-generated set replaces the deterministic chips, labeled as smart.
    await waitFor(() =>
      expect(screen.getByText("Which region drove the highest total amount last quarter?")).toBeInTheDocument(),
    );
    expect(screen.getByText("Smart suggestions (model-generated)")).toBeInTheDocument();
    expect(screen.queryByText("What is the total total amount?")).not.toBeInTheDocument();

    // Revert to the standard set.
    await user.click(screen.getByRole("button", { name: /Show standard/ }));
    await waitFor(() =>
      expect(screen.getByText("What is the total total amount?")).toBeInTheDocument(),
    );
    expect(screen.queryByText(/model-generated/)).not.toBeInTheDocument();
  });

  it("keeps the deterministic strip usable when smart generation fails, with retry", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS });
    postMock.mockRejectedValue(
      new ApiError(502, "Could not generate smart questions right now. Try again.", "model_error"),
    );
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="team" />);

    await screen.findByText("What is the total total amount?");
    await user.click(screen.getByRole("button", { name: /Generate smarter questions/ }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(/could not generate smart questions/i),
    );
    // The deterministic chips and the Ask input remain usable.
    expect(screen.getByText("What is the total total amount?")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Try again/ })).toBeInTheDocument();
  });
});
