"use client";

import { useState } from "react";
import { CheckCircle2, HelpCircle } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";

export interface ClarifyingQuestionCardProps {
  /** The SPECIFIC ambiguity the plan detected, e.g. "Do you mean revenue by total value or by order count?" */
  question: string;
  /** Discrete resolution choices when the ambiguity has an enumerable set; omit for free-text answers. */
  options?: string[];
  /** Fires with the user's answer when Submit is pressed. */
  onSubmit: (answer: string) => void;
  /** Busy state while the answer is being submitted. */
  submitting?: boolean;
  /** When set, the card collapses to a compact "Clarified: …" summary (screen 12's Resolved state). */
  resolvedAnswer?: string;
}

/**
 * Screen 12: Clarifying Question Prompt (screens/12-clarifying-question-prompt.md), rendered inline
 * on the Run Viewer. FEAT-033 (Honest Uncertainty) surfaces a SPECIFIC clarifying question instead
 * of guessing; this card presents it with the discrete options when they exist, or a free-text
 * input, and disables Submit until a response is given.
 *
 * Three states per the screen spec: Awaiting Response (question + controls, Submit disabled until
 * answered), Submitting (spinner, controls disabled), Resolved (collapses to a compact summary).
 */
export function ClarifyingQuestionCard({
  question,
  options = [],
  onSubmit,
  submitting = false,
  resolvedAnswer,
}: ClarifyingQuestionCardProps) {
  const [selected, setSelected] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  // The Resolved state: the answer was submitted and the run resumed, so the card collapses to a
  // compact summary within the step feed.
  if (resolvedAnswer !== undefined) {
    return (
      <div
        role="status"
        data-testid="clarification-resolved"
        className="flex items-center gap-2 rounded-md border border-success/30 bg-success/12 p-3"
      >
        <CheckCircle2 size={16} className="shrink-0 text-success" aria-hidden="true" />
        <p className="text-body-small text-text-primary">
          Clarified: <span className="font-medium">{resolvedAnswer}</span>
        </p>
      </div>
    );
  }

  const freeText = options.length === 0;
  const answer = freeText ? draft.trim() : selected ?? "";
  const canSubmit = answer.length > 0 && !submitting;

  const handleSubmit = () => {
    if (!canSubmit) return;
    onSubmit(answer);
  };

  return (
    <div
      role="alertdialog"
      aria-label="Clarifying question"
      data-testid="clarification-card"
      className="flex flex-col gap-3 rounded-md border border-info/40 bg-info/12 p-4"
    >
      <div className="flex items-start gap-2">
        <HelpCircle size={18} className="mt-[3px] shrink-0 text-info" aria-hidden="true" />
        <p className="text-body font-medium text-text-primary">{question}</p>
      </div>

      {freeText ? (
        <Input
          label="Your answer"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          disabled={submitting}
          placeholder="Type your answer"
          data-testid="clarification-input"
        />
      ) : (
        <div role="radiogroup" aria-label="Resolution" className="flex flex-wrap gap-2">
          {options.map((option) => {
            const active = selected === option;
            return (
              <button
                key={option}
                type="button"
                role="radio"
                aria-checked={active}
                disabled={submitting}
                onClick={() => setSelected(option)}
                data-testid={`clarification-option-${option}`}
                className={`touch-target rounded-md border px-3 text-body-small transition-colors ${
                  active
                    ? "border-info bg-info/20 text-text-primary"
                    : "border-border bg-surface text-text-secondary hover:border-info hover:text-text-primary"
                }`}
              >
                {option}
              </button>
            );
          })}
        </div>
      )}

      <div className="flex justify-end">
        <Button
          variant="primary"
          loading={submitting}
          disabled={!canSubmit}
          onClick={handleSubmit}
        >
          Submit
        </Button>
      </div>
    </div>
  );
}
