"use client";

import { useCallback, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Send } from "lucide-react";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { EngineStatus, SubscriptionTier, type Run } from "@/types/schema";
import { QuestionSuggestions } from "./QuestionSuggestions";

export interface AskPanelProps {
  workspaceId: string;
  /**
   * The live engine status from the parent's poll (Workspace Home's status pill). Wake-on-intent
   * (FEAT-012) fires when the input is focused on a sleeping engine. When absent (parent not wired
   * yet), focusing still fires the wake — the endpoint is idempotent, so waking an already-ready
   * engine is harmless — which is what keeps the feature working standalone.
   */
  engineStatus?: EngineStatus;
  /**
   * Called when a wake request resolves, so the parent's status pill can update immediately instead
   * of waiting for its next poll. The value is the server's own engine_status, so the pill never
   * claims "waking" that the engine did not report.
   */
  onEngineStatusChange?: (status: EngineStatus) => void;
  /** True once the workspace has at least one ingested dataset — shows the FEAT-050 strip. */
  hasDatasets?: boolean;
  /** Read-only shared access (FEAT-045): the suggestions strip is hidden with the Ask input. */
  readOnly?: boolean;
  /** The user's tier for DISPLAY — gates FEAT-051's smart-generation affordance. */
  tier?: SubscriptionTier;
}

interface CreateRunResponse {
  run: Run;
}

interface WakeResponse {
  engine_status: EngineStatus;
}

/**
 * The Ask panel — screens/04-workspace-home.md's "large TextField... with a Submit IconButton"
 * (mobile) / "full-width TextInput + Submit Button" (web).
 *
 * FEAT-013 (Phase 2) makes this control live: submitting a question POSTs to
 * /v1/workspaces/:id/runs, which creates the run in "queued" status and starts the linear executor
 * in the background, then navigates to the Run Viewer to watch it. The run is server-side state —
 * navigation away mid-run is fine, the run continues.
 *
 * FEAT-012 (Phase 5) adds wake-on-intent: focusing the input on a sleeping workspace fires
 * POST /v1/workspaces/:id/engine/wake, so the engine starts waking while the user types. An unused
 * wake (focus then navigate away without asking) is a success — the engine idles out again on its
 * own schedule (user_flow.md Flow 4 Alternate Path A) — and a failed wake is swallowed, never an
 * error state, because the question path itself wakes the engine on submit.
 *
 * The trimmed-empty check mirrors the server's `invalid_question` rejection: the control disables
 * submit for blank input rather than issuing a request that is guaranteed to be rejected.
 */
export function AskPanel({
  workspaceId,
  engineStatus,
  onEngineStatusChange,
  hasDatasets = false,
  readOnly = false,
  tier,
}: AskPanelProps) {
  const router = useRouter();
  const [question, setQuestion] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Guards against duplicate concurrent wake requests from repeated focus events while the first
  // is still in flight. Not a "already woke" latch: after the engine idles back to sleeping, a new
  // focus must be able to wake it again.
  const wakeInFlight = useRef(false);

  const trimmed = question.trim();
  const canSubmit = trimmed.length > 0 && !submitting;

  // Wake-on-intent (FEAT-012): focusing the Ask input is the signal of intent. Skip when the parent
  // says the engine is already waking or ready; otherwise (sleeping, or unknown/not wired) fire the
  // wake. The endpoint is idempotent, so this is safe to call even when the parent has no status.
  const handleFocus = useCallback(() => {
    if (engineStatus === EngineStatus.Waking || engineStatus === EngineStatus.Ready) {
      return;
    }
    if (wakeInFlight.current) return;
    wakeInFlight.current = true;
    void api
      .post<WakeResponse>(`/v1/workspaces/${workspaceId}/engine/wake`)
      .then((response) => {
        // The status the engine actually reports — never a client-invented "waking".
        onEngineStatusChange?.(response.engine_status);
      })
      .catch(() => {
        // An unused wake that fails must not error the UI or leave a bad state (FEAT-012 AC2): the
        // status pill keeps showing whatever the engine reports, and submitting a question wakes
        // the engine on its own path regardless.
      })
      .finally(() => {
        wakeInFlight.current = false;
      });
  }, [engineStatus, workspaceId, onEngineStatusChange]);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (!canSubmit) return;

    setSubmitting(true);
    setError(null);
    try {
      const response = await api.post<CreateRunResponse>(
        `/v1/workspaces/${workspaceId}/runs`,
        { question_text: trimmed },
      );
      router.push(routes.run(workspaceId, response.run.id));
    } catch (caught) {
      const message =
        caught instanceof ApiError
          ? caught.message
          : "Something went wrong. Try again.";
      setError(message);
      setSubmitting(false);
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <form className="flex items-end gap-2" onSubmit={handleSubmit} aria-label="Ask a question">
        <div className="flex-1">
          <Input
            label="Ask a question"
            placeholder="Ask a question about your data..."
            value={question}
            onChange={(event) => setQuestion(event.target.value)}
            onFocus={handleFocus}
            disabled={submitting}
            error={error ?? undefined}
          />
        </div>
        <Button
          type="submit"
          variant="primary"
          disabled={!canSubmit}
          loading={submitting}
          className="shrink-0"
        >
          <Send size={16} aria-hidden="true" />
          <span className="sr-only">Submit question</span>
        </Button>
      </form>

      {/* FEAT-050/FEAT-051: the suggestions strip — deterministic on every tier, smart generation
          gated to Pro/Team. Shown only when the workspace has data and the member may submit
          questions; hidden for read-only access and in the Empty (no data) state. */}
      {hasDatasets && !readOnly && (
        <QuestionSuggestions workspaceId={workspaceId} onFill={setQuestion} tier={tier} />
      )}
    </div>
  );
}
