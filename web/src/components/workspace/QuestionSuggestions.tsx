"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Lightbulb, Lock, Sparkles, Undo2 } from "lucide-react";
import { ErrorState } from "@/components/shared/ErrorState";
import { Button } from "@/components/ui/Button";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { SubscriptionTier } from "@/types/schema";

export interface QuestionSuggestionsProps {
  workspaceId: string;
  /** Fills the Ask input with the clicked suggestion's exact text (editable, never auto-submitted). */
  onFill: (text: string) => void;
  /**
   * The user's tier for DISPLAY — FEAT-051's smart generation is Pro/Team. On Free the smart
   * affordance renders the locked Upgrade Prompt (no request, no engine wake). Display-only; the
   * server is authoritative.
   */
  tier?: SubscriptionTier;
}

interface ListSuggestionsResponse {
  suggestions: string[];
}

interface SmartSuggestionsResponse {
  suggestions: string[];
  model_generated: boolean;
}

type StripState =
  | { status: "loading" }
  | { status: "loaded"; suggestions: string[] }
  | { status: "error"; message: string };

type SmartState =
  | { status: "idle" }
  | { status: "working" }
  | { status: "error"; message: string };

/**
 * FEAT-050 + FEAT-051's question-suggestions strip (screens/04-workspace-home.md, docs commit
 * ef8f3bd): a horizontally scrollable row of suggested-question chips below the Ask input. The
 * deterministic set (FEAT-050) is generated from persisted metadata — no engine, no model — and is
 * the default on every load and every tier.
 *
 * FEAT-051 adds the Pro/Team "Generate smarter questions" affordance: on click it POSTs
 * /suggestions/smart (wake → profile → one egress-logged, metered model call); on success the
 * model-generated set replaces the deterministic chips FOR THE SESSION, labeled as smart, with a
 * revert control. On Free it renders the locked Upgrade Prompt (no request). A failure at any
 * stage leaves the deterministic strip and the Ask input fully usable.
 */
export function QuestionSuggestions({ workspaceId, onFill, tier }: QuestionSuggestionsProps) {
  const [state, setState] = useState<StripState>({ status: "loading" });
  const [smartState, setSmartState] = useState<SmartState>({ status: "idle" });
  const [smartSuggestions, setSmartSuggestions] = useState<string[] | null>(null);
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

  const generateSmart = useCallback(async () => {
    if (smartState.status === "working") return; // no double-generation
    setSmartState({ status: "working" });
    try {
      const response = await api.post<SmartSuggestionsResponse>(
        `/v1/workspaces/${workspaceId}/suggestions/smart`,
      );
      if (!mounted.current) return;
      setSmartSuggestions(response.suggestions ?? []);
      setSmartState({ status: "idle" });
    } catch (error) {
      if (!mounted.current) return;
      // FEAT-051: a failure leaves the deterministic strip usable — keep it, surface the error.
      setSmartState({
        status: "error",
        message: error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [smartState.status, workspaceId]);

  const smartEnabled = tier === SubscriptionTier.Pro || tier === SubscriptionTier.Team;

  // The strip only renders in the loaded-with-suggestions state; loading/error/empty are below.
  const suggestions = smartSuggestions ?? (state.status === "loaded" ? state.suggestions : []);

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
  if (state.suggestions.length === 0 && smartSuggestions === null) {
    return null;
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 text-caption uppercase tracking-wide text-text-secondary">
          <Lightbulb size={12} aria-hidden="true" />
          {smartSuggestions !== null ? "Smart suggestions (model-generated)" : "Suggested questions"}
        </p>
        <div className="flex items-center gap-2">
          {smartSuggestions !== null && (
            <Button
              variant="ghost"
              className="h-7 px-2 text-caption"
              onClick={() => setSmartSuggestions(null)}
            >
              <Undo2 size={12} aria-hidden="true" />
              Show standard
            </Button>
          )}
          {smartEnabled ? (
            <Button
              variant="outline"
              className="h-7 px-2 text-caption"
              onClick={() => void generateSmart()}
              disabled={smartState.status === "working"}
              loading={smartState.status === "working"}
            >
              <Sparkles size={12} aria-hidden="true" />
              Generate smarter questions
            </Button>
          ) : (
            <Link
              href={routes.accountSettings()}
              className="inline-flex items-center gap-1 text-caption text-provenance hover:text-provenance"
            >
              <Lock size={12} aria-hidden="true" />
              Upgrade to Pro/Team
            </Link>
          )}
        </div>
      </div>

      {smartState.status === "working" && (
        <p role="status" className="text-body-small text-text-secondary">
          Waking engine → profiling your data → generating questions…
        </p>
      )}
      {smartState.status === "error" && (
        <div className="flex flex-col gap-2">
          <p role="alert" className="text-body-small text-error">
            {smartState.message}
          </p>
          <Button variant="outline" className="h-7 self-start px-2 text-caption" onClick={() => void generateSmart()}>
            Try again
          </Button>
        </div>
      )}

      <div className="flex gap-2 overflow-x-auto pb-1">
        {suggestions.map((suggestion) => (
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
