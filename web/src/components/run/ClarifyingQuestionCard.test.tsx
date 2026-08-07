import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ClarifyingQuestionCard } from "./ClarifyingQuestionCard";

describe("ClarifyingQuestionCard (screen 12 — FEAT-033)", () => {
  it("renders the specific ambiguity with discrete options and Submit disabled until a response", async () => {
    const onSubmit = vi.fn();
    render(
      <ClarifyingQuestionCard
        question="Do you mean revenue by total value or by order count?"
        options={["By revenue", "By count"]}
        onSubmit={onSubmit}
      />,
    );

    expect(screen.getByTestId("clarification-card")).toHaveTextContent(
      "Do you mean revenue by total value or by order count?",
    );
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeDisabled();

    // Selecting a discrete option enables Submit and fires it with that exact answer.
    await userEvent.click(screen.getByRole("radio", { name: "By revenue" }));
    expect(submit).not.toBeDisabled();
    await userEvent.click(submit);
    expect(onSubmit).toHaveBeenCalledWith("By revenue");
  });

  it("uses a free-text input when the ambiguity has no discrete options", async () => {
    const onSubmit = vi.fn();
    render(
      <ClarifyingQuestionCard
        question="Over what time period?"
        options={[]}
        onSubmit={onSubmit}
      />,
    );

    const input = screen.getByTestId("clarification-input");
    const submit = screen.getByRole("button", { name: "Submit" });
    expect(submit).toBeDisabled();

    await userEvent.type(input, "Q1 2026");
    expect(submit).not.toBeDisabled();
    await userEvent.click(submit);
    expect(onSubmit).toHaveBeenCalledWith("Q1 2026");
  });

  it("shows a busy Submit while the answer is being submitted", () => {
    render(
      <ClarifyingQuestionCard
        question="Which metric?"
        options={["A", "B"]}
        onSubmit={vi.fn()}
        submitting
      />,
    );
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
  });

  it("collapses to a compact Clarified summary once resolved (screen 12 Resolved state)", () => {
    render(
      <ClarifyingQuestionCard
        question="Do you mean revenue by total value or by order count?"
        options={["By revenue", "By count"]}
        onSubmit={vi.fn()}
        resolvedAnswer="By revenue"
      />,
    );

    expect(screen.getByTestId("clarification-resolved")).toHaveTextContent("By revenue");
    expect(screen.queryByTestId("clarification-card")).not.toBeInTheDocument();
  });
});
