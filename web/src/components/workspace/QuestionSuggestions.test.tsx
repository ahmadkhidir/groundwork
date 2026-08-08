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
    api: { ...actual.api, get: vi.fn(), post: vi.fn(), delete: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);
const deleteMock = vi.mocked(api.delete);

const SUGGESTIONS = [
  "What is the total total amount?",
  "What are the most common customer id values?",
  "How does total amount change over time?",
];

const SMART = [
  { id: "sq-1", question: "Which region drove the highest total amount last quarter?" },
  { id: "sq-2", question: "What explains the recent peak in total amount?" },
];

describe("QuestionSuggestions", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
    deleteMock.mockReset();
  });

  it("shows skeleton chips while loading, then the suggestions as chips", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: [] });
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} />);

    expect(screen.getByLabelText("Loading suggested questions")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByText("What is the total total amount?")).toBeInTheDocument(),
    );
    expect(screen.getByText("Suggested questions")).toBeInTheDocument();
  });

  it("fills the Ask input with the clicked suggestion's exact text (never auto-submits)", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: [] });
    const onFill = vi.fn();
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={onFill} />);

    const chip = await screen.findByRole("button", { name: SUGGESTIONS[0] });
    await user.click(chip);

    expect(onFill).toHaveBeenCalledWith(SUGGESTIONS[0]);
    expect(onFill).toHaveBeenCalledTimes(1);
  });

  it("renders nothing when no suggestions are possible (never padded)", async () => {
    getMock.mockResolvedValue({ suggestions: [], smart: [] });
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

    getMock.mockResolvedValueOnce({ suggestions: SUGGESTIONS, smart: [] });
    await user.click(screen.getByRole("button", { name: "Try Again" }));
    await waitFor(() =>
      expect(screen.getByText("What is the total total amount?")).toBeInTheDocument(),
    );
  });

  // ---- FEAT-051 smart generation -------------------------------------------------------------

  it("locks the smart affordance as the Upgrade Prompt on Free — no request, no wake", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: [] });
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="free" />);

    const upgrade = await screen.findByRole("link", { name: /Upgrade to Pro\/Team/ });
    expect(upgrade).toHaveAttribute("href", "/settings");
    expect(screen.queryByRole("button", { name: /Generate smarter questions/ })).not.toBeInTheDocument();
    expect(postMock).not.toHaveBeenCalled();
  });

  it("renders the persisted smart bucket on every tier (viewing is free; generating is gated)", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: SMART });
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="free" />);

    expect(await screen.findByText("Smart questions")).toBeInTheDocument();
    expect(screen.getByText(SMART[0].question)).toBeInTheDocument();
    // The deterministic strip is still visible alongside the bucket.
    expect(screen.getByText(SUGGESTIONS[0])).toBeInTheDocument();
  });

  it("generates smart questions on Pro/Team and APPENDS them to the persisted bucket (no swap, no revert)", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: [] });
    postMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: SMART, model_generated: true });
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="pro" />);

    await screen.findByText("What is the total total amount?");
    await user.click(screen.getByRole("button", { name: /Generate smarter questions/ }));

    expect(postMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/suggestions/smart");
    // The model-generated questions join the list under the Smart questions group...
    await waitFor(() =>
      expect(screen.getByText("Which region drove the highest total amount last quarter?")).toBeInTheDocument(),
    );
    expect(screen.getByText("Smart questions")).toBeInTheDocument();
    // ...and the deterministic strip stays visible — it is not replaced for the session.
    expect(screen.getByText("What is the total total amount?")).toBeInTheDocument();
    // No session-only revert control: the bucket is persisted.
    expect(screen.queryByRole("button", { name: /Show standard/ })).not.toBeInTheDocument();
  });

  it("deletes a smart question from the bucket with its delete control", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: SMART });
    deleteMock.mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="pro" />);

    const chip = await screen.findByText(SMART[0].question);
    await user.click(screen.getByRole("button", { name: `Delete ${SMART[0].question}` }));

    expect(deleteMock).toHaveBeenCalledWith("/v1/workspaces/ws-1/suggestions/sq-1");
    await waitFor(() => expect(chip).not.toBeInTheDocument());
    // The other bucket row and the deterministic strip remain.
    expect(screen.getByText(SMART[1].question)).toBeInTheDocument();
    expect(screen.getByText(SUGGESTIONS[0])).toBeInTheDocument();
  });

  it("treats a 404 delete as already-removed and keeps the chip on other failures", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: SMART });
    deleteMock.mockRejectedValueOnce(new ApiError(404, "Suggestion not found.", "not_found"));
    deleteMock.mockRejectedValueOnce(new ApiError(500, "Something went wrong. Try again."));
    const user = userEvent.setup();
    render(<QuestionSuggestions workspaceId="ws-1" onFill={vi.fn()} tier="pro" />);

    const first = await screen.findByText(SMART[0].question);
    await user.click(screen.getByRole("button", { name: `Delete ${SMART[0].question}` }));
    await waitFor(() => expect(first).not.toBeInTheDocument());

    // A hard failure keeps the chip and says so honestly.
    await user.click(screen.getByRole("button", { name: `Delete ${SMART[1].question}` }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't remove that question. Try again.");
    expect(screen.getByText(SMART[1].question)).toBeInTheDocument();
  });

  it("keeps the deterministic strip usable when smart generation fails, with retry", async () => {
    getMock.mockResolvedValue({ suggestions: SUGGESTIONS, smart: [] });
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
