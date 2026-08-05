"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { AlertTriangle, CheckCircle2 } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { api, subscribeToStream } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { StepIndicator } from "./StepIndicator";
import type { IngestStreamEvent } from "./types";

export interface IngestProgressScreenProps {
  workspaceId: string;
}

/** How long the Success state (screens/06's states table) is shown before auto-navigating. */
const SUCCESS_DISPLAY_MS = 600;

/**
 * Screen 6: Ingest Progress (screens/06-ingest-progress.md) — FEAT-004 (provisioning + loading),
 * FEAT-005 (profiling), and FEAT-008 (relationship inference, when 2+ files).
 *
 * Non-interactive by design (the spec is explicit: "no user action available... intentionally
 * non-interactive but never a bare unlabeled spinner"). The only affordance is "Back to Upload" on
 * failure.
 */
export function IngestProgressScreen({ workspaceId }: IngestProgressScreenProps) {
  const router = useRouter();
  const [event, setEvent] = useState<IngestStreamEvent | null>(null);
  const [connectionError, setConnectionError] = useState(false);
  const [showSuccess, setShowSuccess] = useState(false);
  const navigatedRef = useRef(false);
  // Mirrors `event`'s done-ness for the onError closure below, which is registered once (on mount)
  // and would otherwise only ever see its first render's stale value.
  const doneRef = useRef(false);

  useEffect(() => {
    let cancelled = false;

    // Idempotent on the server (ingest.Pipeline.Start no-ops on anything not "pending"), so this is
    // safe to fire on every mount — including a returning visit after the tab was closed mid-ingest
    // (screens/06's edge case: "ingest continues server-side... rather than losing progress").
    api.post(`/v1/workspaces/${workspaceId}/ingest`).catch(() => {
      // Not fatal on its own — the stream below is this screen's actual source of truth. If there
      // was truly nothing to start, the stream will report `done` immediately with an empty list.
    });

    const unsubscribe = subscribeToStream(`/v1/workspaces/${workspaceId}/ingest/stream`, {
      onOpen: () => {
        if (!cancelled) setConnectionError(false);
      },
      onMessage: (data) => {
        if (cancelled) return;
        try {
          const parsed = JSON.parse(data) as IngestStreamEvent;
          doneRef.current = parsed.done;
          setEvent(parsed);
        } catch {
          // A malformed frame is ignored; the next one corrects the picture rather than tearing
          // down the connection over one bad message.
        }
      },
      onError: () => {
        // The server intentionally closes the stream once `done` (see ingest.go's handleIngestStream),
        // which the browser's EventSource treats as a dropped connection and would otherwise retry —
        // flashing a spurious "Reconnecting…" right at the moment of success, just before this screen
        // navigates away. Suppress it once we already know the batch is done.
        if (!cancelled && !doneRef.current) setConnectionError(true);
      },
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [workspaceId]);

  // On success: show a brief Success state (screens/06's states table — "Brief success state before
  // auto-navigating") rather than navigating instantly, then move on to Ingest Confirmation
  // (screens/06's Data Flow step 3). `done` now fires only once the whole pipeline (FEAT-004 through
  // FEAT-008, when applicable) has genuinely finished — see IngestStreamEvent's doc comment.
  useEffect(() => {
    if (event?.done && event.overall_step !== "failed" && !navigatedRef.current) {
      navigatedRef.current = true;
      setShowSuccess(true);
      const timer = setTimeout(() => {
        router.push(routes.ingestConfirm(workspaceId));
      }, SUCCESS_DISPLAY_MS);
      return () => clearTimeout(timer);
    }
  }, [event, workspaceId, router]);

  const failed = event?.overall_step === "failed";
  const failedDataset = event?.datasets.find((d) => d.step === "failed");
  const failureReason = failedDataset?.error || "Ingest failed for an unspecified reason.";

  return (
    <main id="main-content" className="mx-auto flex w-full max-w-md flex-col gap-6 p-8">
      <Card>
        {failed ? (
          <div className="flex flex-col gap-4">
            <div className="flex items-center gap-2 text-error">
              <AlertTriangle size={20} aria-hidden="true" />
              <h1 className="text-h3">Ingest failed</h1>
            </div>
            {failedDataset && (
              <p className="text-body-small text-text-secondary">
                <span className="font-medium text-text-primary">{failedDataset.filename}:</span>{" "}
                {failureReason}
              </p>
            )}
            {!failedDataset && (
              <p className="text-body-small text-text-secondary">{failureReason}</p>
            )}
            <Button variant="primary" onClick={() => router.push(routes.upload(workspaceId))}>
              Back to Upload
            </Button>
          </div>
        ) : showSuccess ? (
          <div className="flex flex-col items-center gap-3 py-4 text-center" role="status">
            <CheckCircle2 size={28} className="text-success" aria-hidden="true" />
            <p className="text-body font-medium text-text-primary">Your data is ready</p>
          </div>
        ) : (
          <div className="flex flex-col gap-6">
            <CardHeader
              title="Getting your data ready"
              description={
                connectionError
                  ? "Reconnecting… ingest keeps running in the background either way."
                  : "This runs in the background — feel free to leave this page and come back."
              }
            />
            <StepIndicator
              currentStep={event?.overall_step ?? "provisioning"}
              showRelationships={(event?.total_datasets ?? 0) >= 2}
            />
          </div>
        )}
      </Card>
    </main>
  );
}
