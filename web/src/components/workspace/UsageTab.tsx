"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Gauge, HardDrive, Coins } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import { formatBytes } from "@/lib/limits";
import { SubscriptionTier, type UsageRecord } from "@/types/schema";

export interface UsageTabProps {
  workspaceId: string;
  /**
   * The user's tier for DISPLAY, per monetization.md's FEAT-047 row: Free sees totals only, Pro and
   * Team see the detailed per-period breakdown. Presentation over the same data — the usage endpoint
   * returns the full dataset because usage visibility itself is never gated (FEAT-047 AC3).
   */
  tier: SubscriptionTier;
}

interface GetUsageResponse {
  usage: {
    records: UsageRecord[];
    totals: {
      engine_seconds: number;
      storage_bytes: number;
      model_tokens: number;
    };
  };
}

type LoadState =
  | { status: "loading" }
  | { status: "loaded"; usage: GetUsageResponse["usage"] }
  | { status: "error"; message: string };

/** A single current-period stat row: label, icon, value, and the tier's plain-language unit. */
function StatRow({
  icon: Icon,
  label,
  value,
  hint,
}: {
  icon: typeof Gauge;
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-md border border-border p-3">
      <div className="flex items-center gap-3">
        <Icon size={18} className="text-text-secondary" aria-hidden="true" />
        <div>
          <p className="text-body font-medium text-text-primary">{label}</p>
          {hint && <p className="text-caption text-text-secondary">{hint}</p>}
        </div>
      </div>
      <p className="font-mono text-body text-text-primary">{value}</p>
    </div>
  );
}

/**
 * FEAT-047's Usage tab (screens/13-workspace-settings.md): current-period engine-seconds, storage
 * bytes, and model-token usage as simple stat rows. Free tier sees totals only; Pro/Team also see
 * the per-period breakdown (monetization.md's FEAT-047 row).
 *
 * The endpoint returns the most recent period's record as `totals` — the honest "current period"
 * without a billing-period model — and an empty list when nothing is recorded yet, which renders as
 * zero stat rows with a note rather than a fabricated number.
 */
export function UsageTab({ workspaceId, tier }: UsageTabProps) {
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const res = await api.get<GetUsageResponse>(`/v1/workspaces/${workspaceId}/usage`);
      if (!mounted.current) return;
      setState({ status: "loaded", usage: res.usage });
    } catch (error) {
      if (!mounted.current) return;
      setState({
        status: "error",
        message: error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [workspaceId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  if (state.status === "loading") {
    return (
      <Card>
        <CardHeader title="Usage" description="Loading usage for this period…" />
        <div className="flex flex-col gap-3">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      </Card>
    );
  }

  if (state.status === "error") {
    return <ErrorState description={state.message} onRetry={() => void load()} />;
  }

  const { totals, records } = state.usage;
  const detailed = tier !== SubscriptionTier.Free;

  return (
    <Card>
      <CardHeader
        title="Usage"
        description="Engine time, storage, and model tokens consumed by this workspace — what drives its cost."
      />

      <div className="flex flex-col gap-3">
        <StatRow
          icon={Gauge}
          label="Engine time"
          value={`${totals.engine_seconds.toFixed(1)}s`}
          hint="Current period"
        />
        <StatRow
          icon={HardDrive}
          label="Storage"
          value={formatBytes(totals.storage_bytes)}
          hint="Current period"
        />
        <StatRow
          icon={Coins}
          label="Model tokens"
          value={totals.model_tokens.toLocaleString()}
          hint="Current period"
        />
      </div>

      {records.length === 0 ? (
        <p className="mt-4 text-body-small text-text-secondary">
          No usage recorded yet — numbers will appear here as the workspace is used. Usage is
          metered from day one, before billing exists, so cost drivers stay visible.
        </p>
      ) : detailed ? (
        <details className="mt-4">
          <summary className="cursor-pointer text-body-small text-text-secondary hover:text-text-primary">
            View period breakdown
          </summary>
          <table className="mt-3 w-full border-collapse">
            <thead>
              <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
                <th className="px-2 py-2 font-medium">Period</th>
                <th className="px-2 py-2 font-medium">Engine seconds</th>
                <th className="px-2 py-2 font-medium">Storage</th>
                <th className="px-2 py-2 font-medium">Model tokens</th>
              </tr>
            </thead>
            <tbody>
              {records.map((record) => (
                <tr key={record.id} className="border-b border-border last:border-b-0">
                  <td className="px-2 py-2 text-text-primary">
                    {new Date(record.period_start).toLocaleDateString(undefined, {
                      month: "short",
                      year: "numeric",
                    })}
                  </td>
                  <td className="px-2 py-2 font-mono text-text-primary">
                    {record.engine_seconds.toFixed(1)}s
                  </td>
                  <td className="px-2 py-2 font-mono text-text-primary">
                    {formatBytes(record.storage_bytes)}
                  </td>
                  <td className="px-2 py-2 font-mono text-text-primary">
                    {record.model_tokens.toLocaleString()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      ) : null}
    </Card>
  );
}
