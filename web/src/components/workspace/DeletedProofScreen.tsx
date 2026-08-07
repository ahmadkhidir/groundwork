"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Check, CheckCircle2, FileX2 } from "lucide-react";
import { Card } from "@/components/ui/Card";
import { buttonClassNames } from "@/components/ui/Button";
import { Skeleton } from "@/components/shared/Skeleton";
import { ErrorState } from "@/components/shared/ErrorState";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { DeletedCategory, DeletionRecord } from "@/types/schema";

/**
 * sessionStorage key under which the successful DELETE response's proof is carried from Workspace
 * Settings to this screen. Survives a refresh of the proof page in the same tab; the follow-up GET
 * (screens/14's "or a follow-up GET") recovers it when storage is gone.
 */
const DELETION_PROOF_STORAGE_PREFIX = "groundwork:deletion-proof:";

export function deletionProofStorageKey(workspaceId: string): string {
  return `${DELETION_PROOF_STORAGE_PREFIX}${workspaceId}`;
}

/** Stores a successful deletion's proof so the Deletion Proof screen can render after navigation. */
export function storeDeletionProof(workspaceId: string, proof: DeletionRecord): void {
  sessionStorage.setItem(deletionProofStorageKey(workspaceId), JSON.stringify(proof));
}

function readStoredDeletionProof(workspaceId: string): DeletionRecord | null {
  try {
    const raw = sessionStorage.getItem(deletionProofStorageKey(workspaceId));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as DeletionRecord;
    if (
      typeof parsed?.id !== "string" ||
      typeof parsed?.workspace_id !== "string" ||
      !Array.isArray(parsed.deleted_categories)
    ) {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

/** Plain-language labels for the five DeletedCategory values, in screens/14's order. */
const CATEGORY_LABELS: Record<DeletedCategory, string> = {
  object_store: "Object store contents",
  semantic_layer: "Semantic layer",
  reports_and_conversation: "Reports and conversation history",
  query_caches: "Query caches",
  logs: "Logs",
};

function formatTimestamp(iso: string): string {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? iso : parsed.toLocaleString();
}

type ProofState =
  | { status: "loading" }
  | { status: "loaded"; proof: DeletionRecord }
  | { status: "missing" }
  | { status: "error"; message: string };

export interface DeletedProofScreenProps {
  workspaceId: string;
}

/**
 * Screen 14's Deletion Proof page (`/workspaces/:id/deleted`) — the provable evidence of what was
 * deleted and when, per features.md FEAT-043 ("User is shown confirmation of what was deleted and
 * when, as proof of deletion").
 *
 * Data source, per screens/14: the DELETE response carried through sessionStorage, or the follow-up
 * GET /v1/workspaces/:id/deletion when storage is gone. The proof is never fabricated: with no stored
 * proof and no server record, the page shows an honest not-found state rather than inventing one
 * (screens/14's "never a false success" applied to the proof itself).
 */
export function DeletedProofScreen({ workspaceId }: DeletedProofScreenProps) {
  const router = useRouter();
  const [state, setState] = useState<ProofState>({ status: "loading" });

  const load = useCallback(async () => {
    const stored = readStoredDeletionProof(workspaceId);
    if (stored) {
      setState({ status: "loaded", proof: stored });
      return;
    }

    try {
      const res = await api.get<{ deletion: DeletionRecord }>(
        `/v1/workspaces/${workspaceId}/deletion`,
      );
      setState({ status: "loaded", proof: res.deletion });
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        setState({ status: "missing" });
        return;
      }
      setState({
        status: "error",
        message:
          error instanceof Error ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [workspaceId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  if (state.status === "loading") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-xl p-8">
        <Card>
          <div className="flex flex-col items-center gap-3 py-6">
            <Skeleton className="h-12 w-12 rounded-full" />
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-4 w-64" />
          </div>
        </Card>
      </main>
    );
  }

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-xl p-8">
        <ErrorState
          onRetry={() => void load()}
          onGoBack={() => router.push(routes.workspaces())}
        />
      </main>
    );
  }

  if (state.status === "missing") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-xl p-8">
        <Card>
          <div className="flex flex-col items-center gap-3 py-6 text-center">
            <FileX2 size={40} className="text-text-secondary" aria-hidden="true" />
            <h1 className="text-h1 text-text-primary">No deletion record found</h1>
            <p className="max-w-sm text-body-small text-text-secondary">
              This workspace has no deletion proof. Either it was never deleted, or you were not the
              person who deleted it.
            </p>
            <Link href={routes.workspaces()} className={buttonClassNames("primary")}>
              Back to Workspaces
            </Link>
          </div>
        </Card>
      </main>
    );
  }

  return (
    <main id="main-content" className="mx-auto w-full max-w-xl p-8">
      <Card>
        <div className="flex flex-col items-center gap-2 py-6 text-center">
          <CheckCircle2 size={48} className="text-success" aria-hidden="true" />
          <h1 className="text-h1 text-text-primary">Workspace deleted</h1>
          <p className="max-w-sm text-body-small text-text-secondary">
            Every category of derived state in this workspace was destroyed in a single cascade.
          </p>
        </div>

        <ul className="mx-auto w-full max-w-sm space-y-2">
          {state.proof.deleted_categories.map((category) => (
            <li key={category} className="flex items-center gap-2 text-body text-text-primary">
              <Check size={16} className="shrink-0 text-success" aria-hidden="true" />
              {CATEGORY_LABELS[category] ?? category}
            </li>
          ))}
        </ul>

        <p className="mt-6 text-center text-caption text-text-secondary">
          Completed {formatTimestamp(state.proof.completed_at)}
        </p>

        <div className="mt-6 flex justify-center">
          <Link href={routes.workspaces()} className={buttonClassNames("primary")}>
            Back to Workspaces
          </Link>
        </div>
      </Card>
    </main>
  );
}
