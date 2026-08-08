"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Lightbulb } from "lucide-react";
import { ErrorState } from "@/components/shared/ErrorState";
import { api, ApiError } from "@/lib/api-client";

export interface QuestionSuggestionsProps {
  workspaceId: string;
  /** Fills the Ask input with the clicked suggestion's exact text (editable, never auto-submitted). */
  onFill: (text: string) => void;
}

interface ListSuggestionsResponse {
  suggestions: string[];
}

type StripState =
  | { status: "loading" }
  | { status: "loaded"; suggestions: string[] }
  | { status: "error"; message: string };

/**
 * FEAT-050's question-suggestions strip (screens/04-workspace-home.md, docs commit ef8f3bd): a
 * horizontally scrollable row of suggested-question chips below the Ask input, generated
 * deterministically from persisted metadata — no engine query, no model call, no egress. Clicking a
 * chip fills the Ask input with its exact text; the user edits and submits normally.
 *
 * States per FEAT-050: loading (skeleton chips), loaded (chip row; an empty result renders nothing —
 * never padded with duplicates or unsafe columns), error (shared Error State with retry — the Ask
 * input remains usable regardless). Fetched on mount, which covers both a fresh Workspace Home visit
 * and returning from Ingest Confirmation.
 */
export function QuestionSuggestions({ workspaceId, onFill }: QuestionSuggestionsProps) {
  const [state, setState] = useState<StripState>({ status: "loading" });
  const mounted = useRef(true);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const response = await api.get<ListSuggestionsResponse>(
        `/v1/workspaces/${workspaceId}/suggestions`,
      );
      if (!mounted.current) return;
      setState({ status: "loaded", suggestions: response.suggestions ?? [] });
    } catch (error) {
      if (!mounted.current) return;
      setState({
        status: "error",
        message: error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [workspaceId]);

  useEffect(() => {
    mounted.current = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);

  if (state.status === "loading") {
    return (
      <div aria-label="Loading suggested questions" className="flex gap-2">
        {[0, 1, 2, 3].map((i) => (
          <span
            key={i}
            className="h-8 w-40 animate-pulse rounded-full bg-surface-secondary"
            aria-hidden="true"
          />
        ))}
      </div>
    );
  }

  if (state.status === "error") {
    return <ErrorState description={state.message} onRetry={() => void load()} />;
  }

  // Empty (fewer than 5 safe suggestions possible): hidden, never padded.
  if (state.suggestions.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-col gap-1.5">
      <p className="flex items-center gap-1.5 text-caption uppercase tracking-wide text-text-secondary">
        <Lightbulb size={12} aria-hidden="true" />
        Suggested questions
      </p>
      <div className="flex gap-2 overflow-x-auto pb-1">
        {state.suggestions.map((suggestion) => (
          <button
            key={suggestion}
            type="button"
            onClick={() => onFill(suggestion)}
            className="shrink-0 rounded-full border border-border bg-surface-secondary px-3 py-1.5 text-left text-body-small text-text-primary transition-colors hover:border-primary hover:text-text-primary focus:outline-2 focus:outline-primary"
          >
            {suggestion}
          </button>
        ))}
      </div>
    </div>
  );
}
