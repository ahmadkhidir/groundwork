"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Send } from "lucide-react";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { Run } from "@/types/schema";

export interface AskPanelProps {
  workspaceId: string;
}

interface CreateRunResponse {
  run: Run;
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
 * The trimmed-empty check mirrors the server's `invalid_question` rejection: the control disables
 * submit for blank input rather than issuing a request that is guaranteed to be rejected.
 */
export function AskPanel({ workspaceId }: AskPanelProps) {
  const router = useRouter();
  const [question, setQuestion] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const trimmed = question.trim();
  const canSubmit = trimmed.length > 0 && !submitting;

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
    </div>
  );
}