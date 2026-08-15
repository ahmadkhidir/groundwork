"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Lightbulb, Loader2, Lock, Sparkles, X } from "lucide-react";
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

/** One persisted smart question: the id is the delete key, question is the text. */
interface SmartQuestion {
  id: string;
  question: string;
}

interface ListSuggestionsResponse {
  /** The deterministic, metadata-only strip (FEAT-050) — computed at read time, never stored. */
  suggestions: string[];
  /** The workspace's persisted smart-suggestion bucket (FEAT-051) — generated questions that
   * accumulate until the user deletes them. */
  smart: SmartQuestion[];
}

interface SmartSuggestionsResponse extends ListSuggestionsResponse {
  model_generated: boolean;
}

type StripState =
  | { status: "loading" }
  | { status: "loaded"; suggestions: string[]; smart: SmartQuestion[] }
  | { status: "error"; message: string };

type SmartState =
  | { status: "idle" }
  | { status: "working" }
  | { status: "error"; message: string };

/**
 * Shapes a smart-generation failure into something a user can act on.
 *
 * The API client falls back to "Request failed with status N" when the error body is not JSON (a
 * panic or a proxy in front of the API) — that is not a useful message, so a 5xx fallback is
 * replaced with plain language. Every ApiError that carries a request id (the server now echoes it
 * in the error body and the x-request-id header) gets it appended, so the user can report the
 * failure and support can match it against the server request log.
 */
function smartGenerationErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    const base =
      error.status >= 500 && error.message.startsWith("Request failed with status")
        ? "Something went wrong while generating questions. Try again."
        : error.message;
    return error.requestId ? `${base} Reference: ${error.requestId}` : base;
  }
  return "Something went wrong. Try again.";
}

/**
 * FEAT-050 + FEAT-051's question-suggestions strip (screens/04-workspace-home.md, docs commit
 * ef8f3bd): suggested-question chips below the Ask input, laid out MULTILINE (flex-wrap) inside a
 * horizontally scrollable container — an accumulated bucket is long, so it wraps onto several rows
 * instead of one endless single-line scroll.
 *
 * The deterministic set (FEAT-050) is generated from persisted metadata — no engine, no model — and
 * is the always-visible baseline on every load and every tier. FEAT-051's smart generation
 * (Pro/Team) APPENDS its questions to a per-workspace persisted bucket (server table
 * suggestion_questions): the user who picks one of ten is not charged to regenerate the other nine
 * on the next visit, and can delete individual questions from the bucket. The bucket rows are
 * deletable; the deterministic strip is computed, never stored, so it has nothing to delete.
 */
export function QuestionSuggestions({ workspaceId, onFill, tier }: QuestionSuggestionsProps) {
  const [state, setState] = useState<StripState>({ status: "loading" });
  const [smartState, setSmartState] = useState<SmartState>({ status: "idle" });
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const response = await api.get<ListSuggestionsResponse>(
        `/v1/workspaces/${workspaceId}/suggestions`,
      );
      if (!mounted.current) return;
      setState({
        status: "loaded",
        suggestions: response.suggestions ?? [],
        smart: response.smart ?? [],
      });
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
      // FEAT-051 "add to the list": the server appended the new questions to the persisted bucket
      // and returns the full bucket — the deterministic strip stays visible alongside it.
      setState((prev) =>
        prev.status === "loaded"
          ? {
              status: "loaded",
              suggestions: response.suggestions ?? prev.suggestions,
              smart: response.smart ?? prev.smart,
            }
          : prev,
      );
      setSmartState({ status: "idle" });
    } catch (error) {
      if (!mounted.current) return;
      // A failure leaves the deterministic strip and any existing bucket rows usable — keep them,
      // surface the error (with a correlation id when the server supplied one).
      setSmartState({
        status: "error",
        message: smartGenerationErrorMessage(error),
      });
    }
  }, [smartState.status, workspaceId]);

  const handleDelete = useCallback(
    async (id: string) => {
      if (deletingId !== null) return; // one deletion at a time
      setDeletingId(id);
      setDeleteError(null);
      try {
        await api.delete(`/v1/workspaces/${workspaceId}/suggestions/${id}`);
        if (!mounted.current) return;
        // A 404 (already gone) and a 204 both mean the row is not in the bucket anymore.
        setState((prev) =>
          prev.status === "loaded"
            ? { ...prev, smart: prev.smart.filter((s) => s.id !== id) }
            : prev,
        );
      } catch (error) {
        if (!mounted.current) return;
        // Honest failure: keep the chip and say why. A 404 is treated as already-deleted.
        if (error instanceof ApiError && error.status === 404) {
          setState((prev) =>
            prev.status === "loaded"
              ? { ...prev, smart: prev.smart.filter((s) => s.id !== id) }
              : prev,
          );
        } else {
          setDeleteError("Couldn't remove that question. Try again.");
        }
      } finally {
        if (mounted.current) setDeletingId(null);
      }
    },
    [deletingId, workspaceId],
  );

  const smartEnabled = tier === SubscriptionTier.Pro || tier === SubscriptionTier.Team;

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

  const { suggestions, smart } = state;

  // Empty (nothing possible and nothing generated): hidden, never padded.
  if (suggestions.length === 0 && smart.length === 0) {
    return null;
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 text-caption uppercase tracking-wide text-text-secondary">
          <Lightbulb size={12} aria-hidden="true" />
          Suggested questions
        </p>
        <div className="flex items-center gap-2">
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
      {deleteError && (
        <p role="alert" className="text-body-small text-error">
          {deleteError}
        </p>
      )}

      {/* The deterministic strip: computed at read time, never stored, so there is nothing to
          delete. Multiline wrap; horizontal scroll only when a single row overflows. */}
      {suggestions.length > 0 && (
        <div
          aria-label="Suggested questions"
          className="flex max-w-full flex-wrap gap-2 overflow-x-auto pb-1"
        >
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
      )}

      {/* The persisted smart bucket: model-generated questions that accumulate until the user
          deletes them. Each chip fills the input on click and carries its own delete control. */}
      {smart.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <p className="flex items-center gap-1.5 text-caption uppercase tracking-wide text-text-secondary">
            <Sparkles size={12} aria-hidden="true" />
            Smart questions
            <span className="normal-case">(saved — delete the ones you don&apos;t need)</span>
          </p>
          <div
            aria-label="Smart questions"
            className="flex max-w-full flex-wrap gap-2 overflow-x-auto pb-1"
          >
            {smart.map((item) => (
              <span
                key={item.id}
                className="relative inline-flex max-w-full items-center"
              >
                {/* The fill button IS the pill, carrying the same hover treatment as the
                    deterministic chips (border → primary) so both groups feel identical. pr-8
                    clears the overlaid delete control. */}
                <button
                  type="button"
                  onClick={() => onFill(item.question)}
                  className="min-w-0 shrink truncate rounded-full border border-border bg-surface-secondary py-1.5 pl-3 pr-8 text-left text-body-small text-text-primary transition-colors hover:border-primary hover:text-text-primary focus:outline-2 focus:outline-primary"
                >
                  {item.question}
                </button>
                <button
                  type="button"
                  aria-label={`Delete ${item.question}`}
                  onClick={() => void handleDelete(item.id)}
                  disabled={deletingId === item.id}
                  className="absolute right-1 top-1/2 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded-full text-text-secondary transition-colors hover:bg-error/10 hover:text-error focus:outline-2 focus:outline-primary disabled:opacity-50"
                >
                  {deletingId === item.id ? (
                    <Loader2 size={12} className="animate-spin" aria-hidden="true" />
                  ) : (
                    <X size={12} aria-hidden="true" />
                  )}
                </button>
              </span>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
