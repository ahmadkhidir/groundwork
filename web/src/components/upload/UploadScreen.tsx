"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { WifiOff } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { FileDropZone } from "./FileDropZone";
import { SelectedFileList } from "./SelectedFileList";
import { ZeroRetentionNotice } from "./ZeroRetentionNotice";
import { uploadFile } from "./uploadFile";
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

  const hasSubmittableFiles = files.some(
    (f) => f.status === "pending" || f.status === "error",
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
        status: validation.valid ? "pending" : "error",
        progress: 0,
        errorMessage: validation.valid ? undefined : validation.message,
      };
    });
    setFiles((current) => [...current, ...additions]);
  }

  function removeFile(id: string) {
    setFiles((current) => current.filter((f) => f.id !== id));
  }

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

      <SelectedFileList files={files} onRemove={removeFile} />

      <Button
        variant="primary"
        onClick={handleStartIngest}
        loading={submitting}
        disabled={!online || !hasSubmittableFiles}
      >
        Start Ingest
      </Button>

      <ZeroRetentionNotice workspaceId={workspaceId} />
    </main>
  );
}
