import Link from "next/link";
import { History } from "lucide-react";
import { EmptyState } from "@/components/shared/EmptyState";
import { routes } from "@/lib/routes";
import { isPresent, type SemanticCorrection } from "./types";

export interface CorrectionsTabProps {
  workspaceId: string;
  corrections: SemanticCorrection[];
}

function formatTimestamp(iso: string): string {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? iso : parsed.toLocaleString();
}

/**
 * FEAT-019's Corrections tab (screens/15-semantic-layer.md) — a chronological, read-only log of
 * accumulated user corrections. Legitimately empty until FEAT-020 (a later wave) starts capturing
 * corrections during a run; see server/internal/store/postgres/corrections.go's doc comment.
 */
export function CorrectionsTab({ workspaceId, corrections }: CorrectionsTabProps) {
  if (corrections.length === 0) {
    return (
      <EmptyState
        icon={History}
        title="No corrections yet"
        subtitle="They'll appear here as you refine answers — editing generated SQL or a semantic-layer definition during a run creates a durable correction."
      />
    );
  }

  return (
    <>
      {/* Mobile: stacked list. */}
      <ul className="flex flex-col gap-3 md:hidden">
        {corrections.map((c) => (
          <li key={c.id} className="rounded-md border border-border p-3">
            <p className="font-medium text-text-primary">{c.description}</p>
            <p className="mt-1 text-caption text-text-secondary">{formatTimestamp(c.created_at)}</p>
            <RunLink workspaceId={workspaceId} correction={c} />
          </li>
        ))}
      </ul>

      {/* Web: dense table. */}
      <div className="hidden md:block">
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
              <th className="px-2 py-2 font-medium">What was corrected</th>
              <th className="px-2 py-2 font-medium">Original vs. corrected logic</th>
              <th className="px-2 py-2 font-medium">Originating run</th>
              <th className="px-2 py-2 font-medium">When</th>
            </tr>
          </thead>
          <tbody>
            {corrections.map((c) => (
              <tr key={c.id} className="border-b border-border align-top last:border-b-0">
                <td className="px-2 py-2 font-medium text-text-primary">{c.description}</td>
                <td className="px-2 py-2">
                  <div className="flex max-w-md flex-col gap-1 font-mono text-body-small">
                    <code className="text-error line-through decoration-1">{c.original_logic}</code>
                    <code className="text-code-accent">{c.corrected_logic}</code>
                  </div>
                </td>
                <td className="px-2 py-2">
                  <RunLink workspaceId={workspaceId} correction={c} />
                </td>
                <td className="px-2 py-2 text-body-small text-text-secondary">{formatTimestamp(c.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

/**
 * Links to the originating run, or shows a disabled affordance with an explanatory tooltip when the
 * run has since been deleted — screens/15's documented edge case for a pruned run.
 */
function RunLink({ workspaceId, correction }: { workspaceId: string; correction: SemanticCorrection }) {
  if (!isPresent(correction.originating_run_id)) {
    return (
      <span
        className="text-body-small text-text-secondary"
        title="The run this correction came from is no longer available."
      >
        View run (unavailable)
      </span>
    );
  }
  return (
    <Link
      href={routes.run(workspaceId, correction.originating_run_id)}
      className="text-body-small text-primary hover:underline"
    >
      View run
    </Link>
  );
}
