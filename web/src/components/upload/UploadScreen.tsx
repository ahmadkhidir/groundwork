"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { WifiOff } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { FileDropZone } from "./FileDropZone";
import { SelectedFileList } from "./SelectedFileList";
import { ZeroRetentionNotice } from "./ZeroRetentionNotice";
import { DuplicateFilenameDialog } from "./DuplicateFilenameDialog";
import { uploadFile } from "./uploadFile";
import { checkDuplicateFilename } from "./checkDuplicateFilename";
import { useOnlineStatus } from "@/components/auth/useOnlineStatus";
import { validateUploadFile } from "@/lib/limits";
import { routes } from "@/lib/routes";
import type { SelectedFile } from "./types";

export interface UploadScreenProps {
  workspaceId: string;
}

let nextId = 0;

/**
 * Screen 5: File Upload (screens/05-file-upload.md) — FEAT-003, plus FEAT-044's zero-retention
 * notice inline on this same screen per that feature's own spec.
 */
export function UploadScreen({ workspaceId }: UploadScreenProps) {
  const router = useRouter();
  const online = useOnlineStatus();
  const [files, setFiles] = useState<SelectedFile[]>([]);
  const [submitting, setSubmitting] = useState(false);
  // FEAT-049: the id of the SelectedFile currently showing the Duplicate Filename dialog, or null.
  // Only one dialog is shown at a time — colliding files queue behind it in "duplicate" status and
  // are resolved one at a time via SelectedFileList's per-row "Resolve" action.
  const [resolvingDuplicateId, setResolvingDuplicateId] = useState<string | null>(null);

  const hasSubmittableFiles = files.some(
    (f) => f.status === "pending" || f.status === "error",
  );
  // screens/05's Duplicate Filename Detected state: "Start Ingest disabled until every collision is
  // resolved" — an unresolved duplicate is never silently skipped or auto-applied.
  const hasUnresolvedDuplicates = files.some(
    (f) => f.status === "duplicate" || f.status === "checking-duplicate",
  );

  // screens/05's edge case: "User navigates away mid-upload → confirmation prompt... since partial
  // uploads aren't resumed." `beforeunload` is the only cross-browser mechanism for this; in-app
  // navigation (e.g. clicking a nav link) isn't guarded here since App Router has no router-level
  // navigation-block API as of this Next.js version — reported as a residual gap.
  useEffect(() => {
    if (!submitting) return;
    function handler(event: BeforeUnloadEvent) {
      event.preventDefault();
    }
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [submitting]);

  function addFiles(incoming: File[]) {
    const additions: SelectedFile[] = incoming.map((file) => {
      const validation = validateUploadFile(file);
      return {
        id: `f${nextId++}`,
        file,
        // A validly-typed/sized file still needs its FEAT-049 duplicate check before it can be
        // submitted — "checking-duplicate" is a real, visible state, not an invisible pre-step.
        status: validation.valid ? "checking-duplicate" : "error",
        progress: 0,
        errorMessage: validation.valid ? undefined : validation.message,
      };
    });
    setFiles((current) => [...current, ...additions]);

    for (const entry of additions) {
      if (entry.status !== "checking-duplicate") continue;
      void runDuplicateCheck(entry.id, entry.file.name);
    }
  }

  // FEAT-049: checks the new file's name against every existing dataset in the workspace (any
  // ingest_status, including "failed" — the retry case that caused the real incident) BEFORE it is
  // treated as submittable. A collision moves the row to "duplicate" rather than "pending"; Start
  // Ingest stays disabled for it until the Duplicate Filename dialog is explicitly resolved.
  async function runDuplicateCheck(fileId: string, filename: string) {
    try {
      const result = await checkDuplicateFilename(workspaceId, filename);
      setFiles((current) =>
        current.map((f) =>
          f.id === fileId
            ? result.exists && result.existingDatasetId
              ? { ...f, status: "duplicate", duplicate: { existingDatasetId: result.existingDatasetId } }
              : { ...f, status: "pending" }
            : f,
        ),
      );
    } catch {
      // The duplicate check itself failing must not silently let a real collision through — falling
      // back to "pending" here would defeat FEAT-049 on exactly the request that couldn't confirm
      // safety. Surface it as a per-file error instead, same as any other pre-upload failure.
      setFiles((current) =>
        current.map((f) =>
          f.id === fileId
            ? { ...f, status: "error", errorMessage: "Could not check for an existing file with this name. Try again." }
            : f,
        ),
      );
    }
  }

  function removeFile(id: string) {
    setFiles((current) => current.filter((f) => f.id !== id));
    if (resolvingDuplicateId === id) setResolvingDuplicateId(null);
  }

  function handleDuplicateResolved(fileId: string) {
    // Both paths leave the file "pending": Replace has already deleted the prior dataset (via
    // FEAT-048, inside DuplicateFilenameDialog) so the upload proceeds as an ordinary new dataset;
    // Keep both never touched the existing dataset at all — either way, nothing further
    // distinguishes this file's own upload from a non-colliding one.
    setFiles((current) =>
      current.map((f) => (f.id === fileId ? { ...f, status: "pending", duplicate: undefined } : f)),
    );
    setResolvingDuplicateId(null);
  }

  const resolvingFile = files.find((f) => f.id === resolvingDuplicateId);

  // The Duplicate Filename dialog opens automatically the moment a collision is detected — screens/05
  // shows it "before that file proceeds," not behind an extra click — one at a time if multiple
  // files in the same batch collide, queued behind SelectedFileList's per-row "Resolve" action.
  useEffect(() => {
    if (resolvingDuplicateId) return;
    const nextDuplicate = files.find((f) => f.status === "duplicate");
    if (nextDuplicate) setResolvingDuplicateId(nextDuplicate.id);
  }, [files, resolvingDuplicateId]);

  async function handleStartIngest() {
    setSubmitting(true);

    const toUpload = files.filter((f) => f.status === "pending" || f.status === "error");
    await Promise.all(
      toUpload.map(async (entry) => {
        setFiles((current) =>
          current.map((f) =>
            f.id === entry.id ? { ...f, status: "uploading", progress: 0, errorMessage: undefined } : f,
          ),
        );

        try {
          const result = await uploadFile(workspaceId, entry.file, (fraction) => {
            setFiles((current) =>
              current.map((f) => (f.id === entry.id ? { ...f, progress: fraction } : f)),
            );
          });

          setFiles((current) =>
            current.map((f) =>
              f.id === entry.id
                ? result.dataset
                  ? { ...f, status: "success", progress: 1 }
                  : { ...f, status: "error", errorMessage: result.error ?? "Upload failed." }
                : f,
            ),
          );
        } catch (error) {
          setFiles((current) =>
            current.map((f) =>
              f.id === entry.id
                ? {
                    ...f,
                    status: "error",
                    errorMessage:
                      error instanceof Error ? error.message : "Upload failed. Try again.",
                  }
                : f,
            ),
          );
        }
      }),
    );

    setSubmitting(false);
  }

  // Once every selected file has succeeded, hand off to Ingest Progress (FEAT-004, Wave 3) — this
  // wave's job ends at "upload started successfully," per screens/05's Exit Points.
  useEffect(() => {
    if (
      files.length > 0 &&
      files.every((f) => f.status === "success") &&
      !submitting
    ) {
      router.push(routes.ingestProgress(workspaceId));
    }
  }, [files, submitting, workspaceId, router]);

  return (
    <main id="main-content" className="mx-auto flex w-full max-w-2xl flex-col gap-4 p-8">
      <h1 className="text-h1 text-text-primary">Upload data</h1>

      {!online && (
        <div
          role="alert"
          className="flex items-center gap-2 rounded-md border border-error bg-error/10 px-2 py-2 text-body-small text-error"
        >
          <WifiOff size={18} aria-hidden="true" className="shrink-0" />
          <span>You&apos;re offline</span>
        </div>
      )}

      <FileDropZone onFilesChosen={addFiles} disabled={submitting} />

      <SelectedFileList
        files={files}
        onRemove={removeFile}
        onResolveDuplicate={(id) => setResolvingDuplicateId(id)}
      />

      <Button
        variant="primary"
        onClick={handleStartIngest}
        loading={submitting}
        disabled={!online || !hasSubmittableFiles || hasUnresolvedDuplicates}
      >
        Start Ingest
      </Button>

      <ZeroRetentionNotice workspaceId={workspaceId} />

      {resolvingFile && resolvingFile.duplicate && (
        <DuplicateFilenameDialog
          workspaceId={workspaceId}
          filename={resolvingFile.file.name}
          existingDatasetId={resolvingFile.duplicate.existingDatasetId}
          onResolved={() => handleDuplicateResolved(resolvingFile.id)}
          onCancel={() => setResolvingDuplicateId(null)}
        />
      )}
    </main>
  );
}
