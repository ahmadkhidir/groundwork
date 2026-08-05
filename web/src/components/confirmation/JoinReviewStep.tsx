import { Check, GitBranch, X } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { StatusPill } from "@/components/ui/StatusPill";
import { EmptyState } from "@/components/shared/EmptyState";
import { RowError } from "./TypeReviewStep";
import { CandidateJoinStatus } from "@/types/schema";
import { confidenceTier, type ConfirmationCandidateJoin, type ConfirmationColumn } from "./types";
import { ConfidenceBadge } from "./ConfidenceBadge";

/** A join can only ever be decided as accepted or rejected via this step — never reset to pending. */
export type JoinDecision = "accepted" | "rejected";

export interface JoinReviewStepProps {
  joins: ConfirmationCandidateJoin[];
  columnsById: Map<string, ConfirmationColumn>;
  savingIds: Set<string>;
  errors: Record<string, string>;
  onDecide: (joinId: string, status: JoinDecision) => void;
}

function columnLabel(columnsById: Map<string, ConfirmationColumn>, columnId: string): string {
  const col = columnsById.get(columnId);
  if (!col) return columnId;
  return `${col.dataset_filename}: ${col.name}`;
}

function statusPillFor(status: string) {
  switch (status) {
    case CandidateJoinStatus.Accepted:
      return <StatusPill variant="success">accepted</StatusPill>;
    case CandidateJoinStatus.Rejected:
      return <StatusPill variant="error">rejected</StatusPill>;
    default:
      return <StatusPill variant="neutral">pending review</StatusPill>;
  }
}

/**
 * FEAT-009: Candidate Join Review, Accept, Reject — the "Relationships" step/tab of
 * screens/07-ingest-confirmation.md, shown only when 2+ files were ingested.
 *
 * Every join requires an explicit accept/reject action — "Use suggested defaults" never applies
 * here, per FEAT-009's non-negotiable requirement, so this step renders no bulk-accept affordance at
 * all.
 */
export function JoinReviewStep({ joins, columnsById, savingIds, errors, onDecide }: JoinReviewStepProps) {
  if (joins.length === 0) {
    return (
      <EmptyState
        icon={GitBranch}
        title="No relationships found"
        subtitle="No likely joins were found between your uploaded files. You can still ask questions against each file independently."
      />
    );
  }

  return (
    <>
      {/* Mobile: stacked list. */}
      <ul className="flex flex-col gap-3 md:hidden">
        {joins.map((join) => (
          <li key={join.id} className="rounded-md border border-border p-3">
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="font-medium text-text-primary">
                {columnLabel(columnsById, join.source_column_id)} &harr;{" "}
                {columnLabel(columnsById, join.target_column_id)}
              </span>
              {statusPillFor(join.status)}
            </div>
            <ConfidenceBadge tier={confidenceTier(join.confidence_score)} detail={join.basis} />
            <div className="mt-2 flex gap-2">
              <Button
                variant="primary"
                loading={savingIds.has(join.id)}
                onClick={() => onDecide(join.id, CandidateJoinStatus.Accepted)}
              >
                <Check size={16} aria-hidden="true" />
                Accept
              </Button>
              <Button
                variant="outline"
                loading={savingIds.has(join.id)}
                onClick={() => onDecide(join.id, CandidateJoinStatus.Rejected)}
              >
                <X size={16} aria-hidden="true" />
                Reject
              </Button>
            </div>
            {errors[join.id] && <RowError message={errors[join.id]} />}
          </li>
        ))}
      </ul>

      {/* Web: dense table. */}
      <div className="hidden md:block">
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
              <th className="px-2 py-2 font-medium">Source column</th>
              <th className="px-2 py-2 font-medium">Target column</th>
              <th className="px-2 py-2 font-medium">Confidence / basis</th>
              <th className="px-2 py-2 font-medium">Status</th>
              <th className="px-2 py-2 font-medium">Decision</th>
            </tr>
          </thead>
          <tbody>
            {joins.map((join) => (
              <tr key={join.id} className="border-b border-border last:border-b-0">
                <td className="px-2 py-2 font-medium text-text-primary">
                  {columnLabel(columnsById, join.source_column_id)}
                </td>
                <td className="px-2 py-2 font-medium text-text-primary">
                  {columnLabel(columnsById, join.target_column_id)}
                </td>
                <td className="px-2 py-2">
                  <ConfidenceBadge tier={confidenceTier(join.confidence_score)} detail={join.basis} />
                </td>
                <td className="px-2 py-2">{statusPillFor(join.status)}</td>
                <td className="px-2 py-2">
                  <div className="flex gap-2">
                    <Button
                      variant="primary"
                      loading={savingIds.has(join.id)}
                      onClick={() => onDecide(join.id, CandidateJoinStatus.Accepted)}
                    >
                      <Check size={16} aria-hidden="true" />
                      Accept
                    </Button>
                    <Button
                      variant="outline"
                      loading={savingIds.has(join.id)}
                      onClick={() => onDecide(join.id, CandidateJoinStatus.Rejected)}
                    >
                      <X size={16} aria-hidden="true" />
                      Reject
                    </Button>
                  </div>
                  {errors[join.id] && <RowError message={errors[join.id]} />}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
