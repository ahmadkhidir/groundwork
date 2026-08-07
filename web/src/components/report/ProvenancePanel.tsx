"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { X } from "lucide-react";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import type { ProvenanceClaimDetail } from "@/types/schema";

export interface ProvenancePanelProps {
  workspaceId: string;
  reportId: string;
}

interface PanelResponse {
  detail: ProvenanceClaimDetail;
}

/**
 * Screen 10: Provenance Panel — a right-side drawer over the Report View, driven by `?claim=:claimId`
 * (docs/Groundwork/screens/10-provenance-panel.md, FEAT-030).
 *
 * "Provenance data" here is the exact query and rows that backed the figure the user clicked. The
 * drawer fetches the claim detail from the provenance endpoint and shows: the stated figure + its
 * narrative context, the read-only source SQL, and the specific backing rows with the relevant cell
 * highlighted. Closing the panel returns to the unmodified Report View underneath (the URL drops the
 * `?claim=` param).
 *
 * The "source unavailable" state (the underlying query/rows were deleted or the data changed) is an
 * honest message, never an error — screens/10 requires it as a distinct state from a fetch failure.
 */
export function ProvenancePanel({ workspaceId, reportId }: ProvenancePanelProps) {
  const router = useRouter();
  const searchParams = useSearchParams();
  const claimId = searchParams.get("claim");
  const [detail, setDetail] = useState<ProvenanceClaimDetail | null>(null);
  const [status, setStatus] = useState<"loading" | "loaded" | "error">("loading");
  const [errorMessage, setErrorMessage] = useState("");
  const loaded = useRef(false);

  // All hooks run unconditionally (React's rules); the panel merely renders nothing until a claim is
  // present in the URL.
  const load = useCallback(async () => {
    if (!claimId) return;
    setStatus("loading");
    try {
      const { detail } = await api.get<PanelResponse>(
        `/v1/workspaces/${workspaceId}/reports/${reportId}/provenance/${claimId}`,
      );
      loaded.current = true;
      setDetail(detail);
      setStatus("loaded");
    } catch (error) {
      if (error instanceof ApiError) {
        setErrorMessage(error.message);
      } else {
        setErrorMessage("Something went wrong. Try again.");
      }
      setStatus("error");
    }
  }, [workspaceId, reportId, claimId]);

  // Mount-triggered async fetch; see WorkspaceHomeScreen.tsx for why this eslint rule is suppressed
  // for an async fetch in the effect rather than a synchronous in-effect setState.
  useEffect(() => {
    loaded.current = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  const close = useCallback(() => {
    // Drop only the claim param, leaving the Report View underneath untouched.
    const params = new URLSearchParams(searchParams.toString());
    params.delete("claim");
    router.replace(`${window.location.pathname}?${params.toString()}`);
  }, [router, searchParams]);

  // A drawer with no claim in the URL renders nothing; the Report View underneath is unchanged.
  if (!claimId) {
    return null;
  }

  return (
    <aside
      role="dialog"
      aria-modal="true"
      aria-label="Provenance detail"
      className="fixed inset-y-0 right-0 z-50 flex w-[480px] max-w-full flex-col border-l border-border bg-surface shadow-lg"
    >
      <header className="flex items-start justify-between gap-4 border-b p-4">
        <div>
          <p className="text-xs uppercase tracking-wide text-text-secondary">
            Provenance
          </p>
          {detail && (
            <h2 className="mt-1 text-h2 text-text-primary">{detail.claim.stated_value}</h2>
          )}
        </div>
        <button
          onClick={close}
          aria-label="Close provenance panel"
          className="rounded p-1 text-text-secondary hover:bg-surface-secondary hover:text-text-primary"
        >
          <X size={18} aria-hidden="true" />
        </button>
      </header>

      <div className="flex-1 overflow-y-auto p-4">
        {status === "loading" && (
          <div className="space-y-4">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        )}

        {status === "error" && (
          <div className="rounded-lg p-6">
            <ErrorState
              description={errorMessage}
              onRetry={() => void load()}
              onGoBack={() => void close()}
            />
          </div>
        )}

        {status === "loaded" && detail && (
          <div className="space-y-5">
            <p className="text-body text-text-primary">{detail.claim.claim_text}</p>

            <div>
              <h3 className="mb-1 text-body-small font-medium text-text-secondary">
                Source query
              </h3>
              <pre
                aria-label="Source SQL"
                className="max-h-48 overflow-auto whitespace-pre-wrap rounded-md border bg-surface-secondary p-3 text-body-small text-text-primary"
              >
                {detail.claim.source_query}
              </pre>
            </div>

            {detail.source.available ? (
              <div>
                <h3 className="mb-1 text-body-small font-medium text-text-secondary">
                  Source rows
                </h3>
                <div className="overflow-x-auto rounded-md border">
                  <table className="w-full border-collapse text-body-small">
                    <thead>
                      <tr className="bg-surface-secondary">
                        {(detail.source.columns ?? []).map((col) => (
                          <th
                            key={col}
                            className="px-3 py-2 text-left font-medium text-text-secondary"
                          >
                            {col}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {(detail.source.rows ?? []).map((row, ri) => (
                        <tr key={ri} className="border-t">
                          {row.map((cell, ci) => {
                            const highlighted =
                              detail.source.highlight_column === detail.source.columns?.[ci];
                            return (
                              <td
                                key={ci}
                                className={
                                  highlighted
                                    ? "px-3 py-2 bg-provenance-light"
                                    : "px-3 py-2"
                                }
                              >
                                {cell}
                              </td>
                            );
                          })}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            ) : (
              <div className="rounded-md border border-border p-3 text-body-small text-text-secondary">
                This source data is no longer available.
              </div>
            )}

            <Link
              href={routes.run(workspaceId, detail.claim.run_id)}
              onClick={close}
              className="inline-block text-body-small text-provenance hover:text-provenance"
            >
              View full run →
            </Link>
          </div>
        )}
      </div>
    </aside>
  );
}