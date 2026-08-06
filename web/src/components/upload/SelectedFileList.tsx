import { AlertTriangle, FileText, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { formatBytes } from "@/lib/limits";
import { Button } from "@/components/ui/Button";
import type { SelectedFile } from "./types";

export interface SelectedFileListProps {
  files: SelectedFile[];
  onRemove: (id: string) => void;
  /**
   * Opens the Duplicate Filename dialog for a "duplicate"-status entry, per FEAT-049. Optional so
   * this component stays usable in isolation (and in older tests) without wiring the whole flow.
   */
  onResolveDuplicate?: (id: string) => void;
}

/**
 * screens/05-file-upload.md's Selected-files list: filename, size, type icon, remove action — plus
 * this wave's per-file progress bar (Uploading state), inline error (Upload Error state), and
 * FEAT-049's Duplicate Filename Detected row (a warning plus a "Resolve" action that reopens the
 * Duplicate Filename dialog), since all are properties of the same row rather than separate
 * components.
 */
export function SelectedFileList({ files, onRemove, onResolveDuplicate }: SelectedFileListProps) {
  if (files.length === 0) return null;

  return (
    <ul className="flex flex-col gap-2">
      {files.map((entry) => (
        <li
          key={entry.id}
          className={cn(
            "flex flex-col gap-1 rounded-md border p-2",
            entry.status === "error" && "border-error bg-error/10",
            entry.status === "duplicate" && "border-warning bg-warning/12",
            entry.status !== "error" && entry.status !== "duplicate" && "border-border bg-surface",
          )}
        >
          <div className="flex items-center gap-2">
            <FileText size={18} className="shrink-0 text-text-secondary" aria-hidden="true" />
            <div className="min-w-0 flex-1">
              <p className="truncate text-body text-text-primary">{entry.file.name}</p>
              <p className="text-caption text-text-secondary">
                {formatBytes(entry.file.size)}
              </p>
            </div>
            {entry.status !== "success" && (
              <button
                type="button"
                onClick={() => onRemove(entry.id)}
                aria-label={`Remove ${entry.file.name}`}
                disabled={entry.status === "uploading"}
                className="touch-target flex shrink-0 items-center justify-center text-text-secondary hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
              >
                <X size={18} aria-hidden="true" />
              </button>
            )}
            {entry.status === "success" && (
              <span className="text-caption font-medium text-success">Uploaded</span>
            )}
          </div>

          {entry.status === "uploading" && (
            <div
              role="progressbar"
              aria-valuenow={Math.round(entry.progress * 100)}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-label={`Uploading ${entry.file.name}`}
              className="h-1 w-full overflow-hidden rounded-full bg-surface-secondary"
            >
              <div
                className="h-full bg-primary transition-[width]"
                style={{ width: `${Math.round(entry.progress * 100)}%` }}
              />
            </div>
          )}

          {entry.status === "error" && entry.errorMessage && (
            <p role="alert" className="text-body-small text-error">
              {entry.errorMessage}
            </p>
          )}

          {entry.status === "checking-duplicate" && (
            <p className="text-caption text-text-secondary">Checking for an existing file with this name…</p>
          )}

          {entry.status === "duplicate" && (
            <div className="flex items-center justify-between gap-2">
              <p className="flex items-center gap-1 text-body-small text-warning">
                <AlertTriangle size={14} aria-hidden="true" />A file named &quot;{entry.file.name}&quot; already
                exists in this workspace.
              </p>
              {onResolveDuplicate && (
                <Button variant="outline" onClick={() => onResolveDuplicate(entry.id)}>
                  Resolve
                </Button>
              )}
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}
