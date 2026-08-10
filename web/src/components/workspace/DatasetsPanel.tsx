"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { Database, Trash2, UploadCloud } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { buttonClassNames } from "@/components/ui/Button";
import { StatusPill } from "@/components/ui/StatusPill";
import { EmptyState } from "@/components/shared/EmptyState";
import { Skeleton } from "@/components/shared/Skeleton";
import { DeleteConfirmationModal } from "@/components/shared/DeleteConfirmationModal";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { IngestStatus } from "@/types/schema";

export interface DatasetsPanelProps {
  workspaceId: string;
  /** Read-only shared access (FEAT-045) hides the per-row delete action entirely. */
  readOnly?: boolean;
  /**
   * Called after a dataset is successfully deleted, so the parent (Workspace Home) can react —
   * e.g. reverting `has_datasets` once the workspace's last dataset is removed, per
   * screens/04-workspace-home.md's Data Flow step 6.
   */
  onDatasetDeleted?: (remainingCount: number) => void;
}

/** One row of the Datasets panel, per GET /v1/workspaces/:id/datasets. */
interface DatasetSummary {
  id: string;
  original_filename: string;
  ingest_status: string;
  row_count: number;
  created_at: string;
}

interface ListDatasetsResponse {
  datasets: DatasetSummary[];
}

/** One named dependent relationship, per GET .../datasets/:id/deletion-impact. */
interface DependentRelationship {
  id: string;
  source_column_name: string;
  target_column_name: string;
}

export interface DeletionImpact {
  dataset: DatasetSummary;
  column_count: number;
  dependent_relationships: DependentRelationship[];
  has_reviewed_pii: boolean;
  has_type_override_risk: boolean;
}

type PanelState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; datasets: DatasetSummary[] };

/** Modal state for the delete flow: closed, fetching impact, ready to confirm, or deleting. */
type ModalState =
  | { phase: "closed" }
  | { phase: "loading-impact"; dataset: DatasetSummary }
  | { phase: "ready"; dataset: DatasetSummary; impact: DeletionImpact }
  | { phase: "deleting"; dataset: DatasetSummary; impact: DeletionImpact }
  | { phase: "error"; dataset: DatasetSummary; impact: DeletionImpact; message: string };

function statusPillFor(status: string) {
  switch (status) {
    case IngestStatus.Complete:
      return <StatusPill variant="success">complete</StatusPill>;
    case IngestStatus.Failed:
      return <StatusPill variant="error">failed</StatusPill>;
    default:
      return <StatusPill variant="info">{status}</StatusPill>;
  }
}

function formatUploadedAt(iso: string): string {
  try {
    return new Date(iso).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  } catch {
    return iso;
  }
}

/**
 * Builds the shared Delete Confirmation Modal's disclosure list from a deletion-impact payload —
 * FEAT-048's non-negotiable requirement that the confirmation name what is lost, specifically, never
 * a bare "are you sure?".
 */
export function buildDeletionDisclosure(impact: DeletionImpact): {
  items: string[];
  reviewWorkWarning?: string;
} {
  const items = [
    `The dataset "${impact.dataset.original_filename}" and all ${impact.column_count} of its columns`,
    "Both stored copies: the original uploaded file and its normalized version",
  ];
  if (impact.dependent_relationships.length > 0) {
    const names = impact.dependent_relationships
      .map((rel) => `${rel.source_column_name} ↔ ${rel.target_column_name}`)
      .join(", ");
    items.push(
      `${impact.dependent_relationships.length} confirmed relationship${
        impact.dependent_relationships.length === 1 ? "" : "s"
      } in the semantic layer: ${names}`,
    );
  }

  let reviewWorkWarning: string | undefined;
  if (impact.has_reviewed_pii || impact.has_type_override_risk) {
    reviewWorkWarning =
      "Any column type corrections and PII review decisions recorded on this dataset will be " +
      "permanently lost. They will NOT be carried forward if you upload this file again.";
  }

  return { items, reviewWorkWarning };
}

/**
 * screens/04-workspace-home.md's Datasets panel — FEAT-048. Lists this workspace's datasets
 * (filename, upload time, row count, ingest status) with a per-row delete action that opens the
 * shared Delete Confirmation Modal, naming exactly what will be destroyed before the user commits.
 *
 * The header also carries the persistent "Add data" affordance linking to File Upload. The spec's
 * "Upload data" prompt on Workspace Home is explicitly an empty-state CTA (hidden once
 * `has_datasets` is true, per screens/04's Data Flow step 5), yet the product's own flows require
 * adding more data to a workspace that already has some — FEAT-003 ("multiple files into the same
 * workspace"), Flow 2/Flow 3 (workspaces with several files), and FEAT-049 ("re-uploading a
 * corrected spreadsheet... an entirely ordinary action"). The Datasets panel is that entry point,
 * and being rendered on every viewport ("Both (collapsible on mobile)") it keeps File Upload
 * reachable on mobile too, where the empty-state CTA no longer exists.
 *
 * Two datasets sharing an original_filename (FEAT-049's "Keep both" outcome) are distinguished here
 * by their upload time, since the list is already ordered most-recent-first and each row shows its
 * own `created_at`.
 */
export function DatasetsPanel({ workspaceId, readOnly = false, onDatasetDeleted }: DatasetsPanelProps) {
  const [state, setState] = useState<PanelState>({ status: "loading" });
  const [modal, setModal] = useState<ModalState>({ phase: "closed" });

  // FEAT-045: read-only shared members cannot upload files — the affordance is hidden, exactly as
  // screens/04 hides the Ask input and Upload CTA for them.
  const addDataAction = readOnly ? null : (
    <Link
      href={routes.upload(workspaceId)}
      className={buttonClassNames("secondary")}
    >
      <UploadCloud size={16} aria-hidden="true" />
      Add data
    </Link>
  );

  const load = useCallback(async () => {
    try {
      const res = await api.get<ListDatasetsResponse>(`/v1/workspaces/${workspaceId}/datasets`);
      setState({ status: "loaded", datasets: res.datasets ?? [] });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  async function openDeleteModal(dataset: DatasetSummary) {
    setModal({ phase: "loading-impact", dataset });
    try {
      const impact = await api.get<DeletionImpact>(
        `/v1/workspaces/${workspaceId}/datasets/${dataset.id}/deletion-impact`,
      );
      setModal({ phase: "ready", dataset, impact });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Could not load what this deletion affects. Try again.";
      // No impact payload to show yet — fall back to closing rather than a modal with nothing to say.
      setModal({ phase: "closed" });
      setState((current) =>
        current.status === "loaded" ? current : { status: "error", message },
      );
    }
  }

  function closeModal() {
    setModal({ phase: "closed" });
  }

  async function confirmDelete() {
    if (modal.phase !== "ready" && modal.phase !== "error") return;
    const { dataset, impact } = modal;
    setModal({ phase: "deleting", dataset, impact });
    try {
      await api.delete(`/v1/workspaces/${workspaceId}/datasets/${dataset.id}`);
      setModal({ phase: "closed" });
      setState((current) => {
        if (current.status !== "loaded") return current;
        const remaining = current.datasets.filter((d) => d.id !== dataset.id);
        onDatasetDeleted?.(remaining.length);
        return { status: "loaded", datasets: remaining };
      });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Could not delete this dataset. Try again.";
      setModal({ phase: "error", dataset, impact, message });
    }
  }

  if (state.status === "loading") {
    return (
      <Card>
        <CardHeader title="Datasets" action={addDataAction} />
        <div className="flex flex-col gap-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </Card>
    );
  }

  if (state.status === "error") {
    return (
      <Card>
        <CardHeader title="Datasets" action={addDataAction} />
        <p className="text-body-small text-error">{state.message}</p>
      </Card>
    );
  }

  const modalOpen = modal.phase !== "closed" && modal.phase !== "loading-impact";
  const disclosure =
    modal.phase === "ready" || modal.phase === "deleting" || modal.phase === "error"
      ? buildDeletionDisclosure(modal.impact)
      : null;

  return (
    <Card>
      <CardHeader
        title="Datasets"
        description={`${state.datasets.length} uploaded`}
        action={addDataAction}
      />

      {state.datasets.length === 0 ? (
        <EmptyState icon={Database} title="No datasets yet" subtitle="Upload a file to see it here." />
      ) : (
        <ul className="flex flex-col gap-1">
          {state.datasets.map((ds) => (
            <li
              key={ds.id}
              className="flex items-center justify-between gap-2 rounded-md px-2 py-2 hover:bg-surface-secondary"
            >
              <div className="min-w-0">
                <p className="truncate text-body-small font-medium text-text-primary">
                  {ds.original_filename}
                </p>
                <p className="text-caption text-text-secondary">
                  {formatUploadedAt(ds.created_at)}
                  {ds.ingest_status === IngestStatus.Complete && ` · ${ds.row_count.toLocaleString()} rows`}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {statusPillFor(ds.ingest_status)}
                {!readOnly && (
                  <button
                    type="button"
                    aria-label={`Delete ${ds.original_filename}`}
                    onClick={() => void openDeleteModal(ds)}
                    disabled={modal.phase === "loading-impact" && modal.dataset.id === ds.id}
                    className="touch-target rounded-md p-1 text-text-secondary hover:bg-error/12 hover:text-error disabled:opacity-50"
                  >
                    <Trash2 size={16} aria-hidden="true" />
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      <DeleteConfirmationModal
        open={modalOpen}
        title={
          modal.phase !== "closed" && modal.phase !== "loading-impact"
            ? `Delete "${modal.dataset.original_filename}"?`
            : ""
        }
        items={disclosure?.items ?? []}
        reviewWorkWarning={disclosure?.reviewWorkWarning}
        onCancel={closeModal}
        onConfirm={() => void confirmDelete()}
        loading={modal.phase === "deleting"}
        error={modal.phase === "error" ? modal.message : undefined}
      />
    </Card>
  );
}
