"use client";

import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import {
  Braces,
  CheckCircle2,
  FileCode,
  FileImage,
  FileSpreadsheet,
  FileText,
  Loader2,
  Lock,
  Presentation,
  X,
  type LucideIcon,
} from "lucide-react";
import { Button } from "@/components/ui/Button";
import { apiUrl, ApiError } from "@/lib/api-client";
import { QueryParam, routes } from "@/lib/routes";
import { SubscriptionTier } from "@/types/schema";
import { cn } from "@/lib/cn";

export interface ExportDialogProps {
  workspaceId: string;
  reportId: string;
  /**
   * The user's tier for DISPLAY: which format rows are locked. Display-only — the authoritative gate
   * is server-side (UserTierLookup in export.go), and a client-claimed tier must never be trusted
   * for gating. Defaults to Free (the most restrictive display) when the page does not pass one.
   */
  tier?: SubscriptionTier;
  /**
   * False for a narrative-only report (screens/11 edge case: "Report contains no chart → Image
   * export format is omitted from the list entirely"). FEAT-040's Image row honors this: a report
   * with no execute facts has nothing to export as a chart image, so the row is omitted rather
   * than shown and then failing.
   */
  hasChart?: boolean;
}

/**
 * The formats the dialog can request. The "markup" id is the Markdown/HTML ROW (screens/11 lists
 * "Markdown/HTML" as ONE format option); the actual format= value is resolved at export time via
 * `markupVariant` — markdown or html — because the two are distinct server formats behind one row.
 */
type ExportFormatId = "pdf" | "csv" | "pptx" | "markup" | "json" | "image";

/** The Markdown/HTML row's variant picker value (resolved to the real format= param). */
type MarkupVariant = "markdown" | "html";

type DialogStatus = "idle" | "exporting" | "ready" | "error";

interface FormatOption {
  id: ExportFormatId;
  name: string;
  description: string;
  icon: LucideIcon;
  /** The lowest tier that may use this format, per monetization.md Feature Gate Mapping (FEAT-035 PDF: Free; FEAT-036 CSV: Pro; FEAT-037 PowerPoint: Team; FEAT-038/039/040: Pro). */
  minTier: SubscriptionTier;
}

const FORMAT_OPTIONS: FormatOption[] = [
  {
    id: "pdf",
    name: "PDF",
    description: "Narrative and rendered chart, print-ready.",
    icon: FileText,
    minTier: SubscriptionTier.Free,
  },
  {
    id: "csv",
    name: "Excel/CSV",
    description: "The underlying result set as a CSV spreadsheet.",
    icon: FileSpreadsheet,
    minTier: SubscriptionTier.Pro,
  },
  {
    id: "pptx",
    name: "PowerPoint",
    description: "Charts and narrative as an editable slide deck.",
    icon: Presentation,
    minTier: SubscriptionTier.Team,
  },
  {
    id: "markup",
    name: "Markdown/HTML",
    description: "For wikis and internal docs, with the chart embedded.",
    icon: FileCode,
    minTier: SubscriptionTier.Pro,
  },
  {
    id: "json",
    name: "JSON",
    description: "Structured report data for programmatic use.",
    icon: Braces,
    minTier: SubscriptionTier.Pro,
  },
  {
    id: "image",
    name: "Chart image",
    description: "The chart as a standalone SVG image file.",
    icon: FileImage,
    minTier: SubscriptionTier.Pro,
  },
];

/** Tier ordering for the display gate. Free < Pro < Team. */
const TIER_RANK: Record<SubscriptionTier, number> = {
  free: 0,
  pro: 1,
  team: 2,
};

function canUseFormat(option: FormatOption, tier: SubscriptionTier): boolean {
  return TIER_RANK[tier] >= TIER_RANK[option.minTier];
}

/** How long the "Export ready" state stays visible before the dialog auto-closes. */
const READY_CLOSE_DELAY_MS = 1200;

/**
 * Screen 11: Export Dialog — a modal over the Report View driven by `?export=1` URL state, exactly
 * as the Provenance Panel uses `?claim=` (docs/Groundwork/screens/11-export-dialog.md, FEAT-035/036).
 *
 * Format rows: PDF is available on every tier; Excel/CSV is Pro/Team. A locked row is NEVER hidden —
 * monetization.md's "always show what you're missing" — it renders reduced-emphasis with an Upgrade
 * Prompt and is not selectable at all (screens/11 edge case: clicking the locked row does nothing and
 * the Export button stays disabled for it; the server independently enforces the gate with a 403).
 *
 * Export flow: select a format → Export → the endpoint is fetched as a blob with the session cookie
 * (credentials: "include" — a plain anchor cannot carry the cross-origin cookie), the file downloads,
 * and the dialog shows a brief success state then closes. Closing the dialog during an in-flight
 * export does NOT cancel the job (screens/11 edge case): the fetch continues and the download still
 * fires.
 */
export function ExportDialog({
  workspaceId,
  reportId,
  tier = SubscriptionTier.Free,
  hasChart = true,
}: ExportDialogProps) {
  const searchParams = useSearchParams();
  const isOpen = searchParams.get(QueryParam.Export) !== null;

  // The dialog content is only mounted while the export param is present, so its state (selection,
  // status) initializes fresh on every open with no reset effect needed. A dialog with no export
  // param in the URL renders nothing; the Report View underneath is unchanged.
  if (!isOpen) {
    return null;
  }

  return (
    <ExportDialogContent
      workspaceId={workspaceId}
      reportId={reportId}
      tier={tier}
      hasChart={hasChart}
    />
  );
}

/**
 * The dialog's stateful body. Mounted only while `?export=1` is present — unmounting on close is
 * what resets selection/status for the next open.
 */
function ExportDialogContent({
  workspaceId,
  reportId,
  tier = SubscriptionTier.Free,
  hasChart = true,
}: ExportDialogProps) {
  const router = useRouter();
  const searchParams = useSearchParams();

  const [selectedFormat, setSelectedFormat] = useState<ExportFormatId | null>(null);
  // Markdown/HTML is one row (screens/11); this resolves which of the two server formats it means.
  const [markupVariant, setMarkupVariant] = useState<MarkupVariant>("markdown");
  const [status, setStatus] = useState<DialogStatus>("idle");
  const [errorMessage, setErrorMessage] = useState("");
  const mountedRef = useRef(true);
  const closeTimerRef = useRef<number | null>(null);

  const close = useCallback(() => {
    // Drop only the export param, leaving the Report View underneath untouched — same URL discipline
    // as the provenance panel.
    const params = new URLSearchParams(searchParams.toString());
    params.delete(QueryParam.Export);
    router.replace(`${window.location.pathname}?${params.toString()}`);
  }, [router, searchParams]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (closeTimerRef.current !== null) window.clearTimeout(closeTimerRef.current);
    };
  }, []);

  // Escape closes the dialog. The listener exists only while the dialog is mounted (it is mounted
  // only while open).
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") close();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [close]);

  async function runExport() {
    if (!selectedFormat) return;
    // The Markdown/HTML row resolves to markdown or html at export time — one dialog row, two
    // server formats (FEAT-038).
    const formatParam: ResolvedExportFormat =
      selectedFormat === "markup" ? markupVariant : selectedFormat;
    setStatus("exporting");
    setErrorMessage("");
    try {
      await downloadExportFile(
        `/v1/workspaces/${workspaceId}/reports/${reportId}/export?format=${formatParam}`,
        defaultFilename(formatParam),
      );
      // Closing during export does not cancel the job — the download above already fired. State
      // updates are skipped once unmounted.
      if (!mountedRef.current) return;
      setStatus("ready");
      closeTimerRef.current = window.setTimeout(() => close(), READY_CLOSE_DELAY_MS);
    } catch (err) {
      if (!mountedRef.current) return;
      setErrorMessage(
        err instanceof ApiError ? err.message : "Export failed. Try again.",
      );
      setStatus("error");
    }
  }

  const exporting = status === "exporting";

  return (
    <div
      className="fixed inset-0 z-50 flex items-end justify-center bg-text-primary/40 sm:items-center sm:p-4"
      onClick={close}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="export-dialog-title"
        className="flex max-h-[90vh] w-full flex-col rounded-t-lg border border-border bg-surface shadow-lg sm:max-w-md sm:rounded-md"
        onClick={(event) => event.stopPropagation()}
      >
        <header className="flex items-center justify-between gap-4 border-b p-4">
          <h2 id="export-dialog-title" className="text-h3 text-text-primary">
            Export report
          </h2>
          <button
            onClick={close}
            aria-label="Close export dialog"
            className="touch-target rounded p-1 text-text-secondary hover:bg-surface-secondary hover:text-text-primary"
          >
            <X size={18} aria-hidden="true" />
          </button>
        </header>

        <div className="flex-1 overflow-y-auto p-4">
          <div role="radiogroup" aria-label="Export format" className="flex flex-col gap-3">
            {/* screens/11 edge case: a narrative-only report has no chart to export, so the Image
                row is OMITTED entirely for it — never shown and then failing. */}
            {FORMAT_OPTIONS.filter((option) => option.id !== "image" || hasChart).map((option) => {
              const available = canUseFormat(option, tier);
              const selected = selectedFormat === option.id;
              const description =
                option.id === "pdf" && !hasChart
                  ? "Narrative text, print-ready. This report has no chart."
                  : option.description;
              const Icon = option.icon;

              return (
                <Fragment key={option.id}>
                  {available ? (
                    <button
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      onClick={() => setSelectedFormat(option.id)}
                      className={cn(
                        "flex w-full items-start gap-3 rounded-md border p-4 text-left",
                        selected
                          ? "border-primary bg-primary-light/40"
                          : "border-border hover:bg-surface-secondary",
                      )}
                    >
                      <Icon size={20} className="mt-0.5 shrink-0 text-text-secondary" aria-hidden="true" />
                      <span className="min-w-0 flex-1">
                        <span className="block font-medium text-text-primary">{option.name}</span>
                        <span className="block text-body-small text-text-secondary">{description}</span>
                        {exporting && selected && (
                          <span className="mt-2 flex items-center gap-2 text-body-small text-text-secondary">
                            <Loader2 size={14} className="animate-spin" aria-hidden="true" />
                            Preparing your export…
                          </span>
                        )}
                      </span>
                    </button>
                  ) : (
                    // Locked format row: reduced emphasis, upgrade prompt, NOT selectable — never
                    // hidden (monetization.md's "always show what you're missing").
                    <div
                      aria-disabled="true"
                      className="flex w-full items-start gap-3 rounded-md border border-border p-4 opacity-70"
                    >
                      <Icon size={20} className="mt-0.5 shrink-0 text-text-secondary" aria-hidden="true" />
                      <span className="min-w-0 flex-1">
                        <span className="block font-medium text-text-primary">{option.name}</span>
                        <span className="block text-body-small text-text-secondary">{description}</span>
                        <Link
                          href={routes.settings(workspaceId)}
                          className="mt-1 inline-flex items-center gap-1 text-body-small text-provenance hover:text-provenance"
                        >
                          <Lock size={12} aria-hidden="true" />
                          Upgrade to Pro/Team
                        </Link>
                      </span>
                    </div>
                  )}

                  {/* The Markdown/HTML row is one format option (screens/11) but maps to two server
                      formats; the variant picker appears only while that row is selected. It sits
                      OUTSIDE the radio button (interactive content cannot nest in a button). */}
                  {available && selected && option.id === "markup" && (
                    <div
                      role="group"
                      aria-label="Markdown or HTML variant"
                      className="ml-11 flex items-center gap-2 rounded-md border border-border bg-surface-secondary px-3 py-2"
                    >
                      <label
                        htmlFor="markup-variant"
                        className="text-body-small text-text-secondary"
                      >
                        Format:
                      </label>
                      <select
                        id="markup-variant"
                        aria-label="Markdown or HTML variant"
                        value={markupVariant}
                        onChange={(event) =>
                          setMarkupVariant(event.target.value as MarkupVariant)
                        }
                        className="rounded border border-border bg-surface px-2 py-1 text-body-small text-text-primary"
                      >
                        <option value="markdown">Markdown (.md)</option>
                        <option value="html">HTML (.html)</option>
                      </select>
                    </div>
                  )}
                </Fragment>
              );
            })}
          </div>

          {status === "ready" && (
            <p
              role="status"
              className="mt-3 flex items-center gap-2 text-body-small text-success"
            >
              <CheckCircle2 size={16} aria-hidden="true" />
              Export ready — check your downloads.
            </p>
          )}

          {status === "error" && errorMessage && (
            <div
              role="alert"
              className="mt-3 flex items-start justify-between gap-3 rounded-md border border-error/40 bg-error/10 p-3"
            >
              <p className="text-body-small text-error">{errorMessage}</p>
              <Button
                variant="outline"
                onClick={() => void runExport()}
                className="shrink-0"
              >
                Try Again
              </Button>
            </div>
          )}
        </div>

        <footer className="border-t p-4">
          <Button
            variant="primary"
            className="w-full"
            onClick={() => void runExport()}
            disabled={!selectedFormat}
            loading={exporting}
          >
            Export
          </Button>
        </footer>
      </div>
    </div>
  );
}

/** The format= value actually sent to the server (the markup row resolves to one of these). */
type ResolvedExportFormat = "pdf" | "csv" | "pptx" | "markdown" | "html" | "json" | "image";

function defaultFilename(format: ResolvedExportFormat): string {
  switch (format) {
    case "csv":
      return "groundwork-report-results.csv";
    case "pptx":
      return "groundwork-report.pptx";
    case "markdown":
      return "groundwork-report.md";
    case "html":
      return "groundwork-report.html";
    case "json":
      return "groundwork-report.json";
    case "image":
      return "groundwork-report-chart.svg";
    default:
      return "groundwork-report.pdf";
  }
}

/**
 * Fetches an export endpoint as a blob and triggers a browser download.
 *
 * A plain `<a href>` cannot be used: the Go API is cross-origin in development, and a navigation
 * anchor would not attach the Auth.js session cookie. Fetching with `credentials: "include"` (the
 * same mode the api-client uses) carries the cookie, and the resulting blob is downloaded via a
 * temporary object URL. The server's Content-Disposition filename is honored when present.
 *
 * @throws {ApiError} on any non-2xx response, shaped like the api-client's errors.
 */
export async function downloadExportFile(
  path: string,
  fallbackFilename: string,
): Promise<void> {
  const response = await fetch(apiUrl(path), {
    credentials: "include",
    cache: "no-store",
  });

  if (!response.ok) {
    let message = `Request failed with status ${response.status}`;
    let code = "";
    try {
      const parsed = (await response.json()) as {
        error?: string;
        message?: string;
        code?: string;
      };
      message = parsed.error ?? parsed.message ?? message;
      code = parsed.code ?? "";
    } catch {
      // A non-JSON error body must not mask the real status; keep the status-derived message.
    }
    throw new ApiError(
      response.status,
      message,
      code,
      response.headers.get("x-request-id") ?? "",
    );
  }

  const filename =
    contentDispositionFilename(response.headers.get("content-disposition")) ??
    fallbackFilename;

  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

/** Extracts the filename from a Content-Disposition header like `attachment; filename="x.pdf"`. */
function contentDispositionFilename(header: string | null): string | undefined {
  if (!header) return undefined;
  const match = /filename="([^"]+)"/.exec(header);
  return match?.[1];
}
