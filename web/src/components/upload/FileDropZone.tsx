"use client";

import { useId, useRef, useState, type DragEvent } from "react";
import { UploadCloud } from "lucide-react";
import { cn } from "@/lib/cn";
import { ACCEPTED_UPLOAD_EXTENSIONS } from "@/lib/limits";

export interface FileDropZoneProps {
  onFilesChosen: (files: File[]) => void;
  disabled?: boolean;
}

/**
 * screens/05-file-upload.md's drop zone / picker: a large dashed-border drop target with
 * click-to-browse fallback on web, and the same tappable area driving the native file picker on
 * mobile (there is no separate mobile-only variant — `<input type="file">` already opens the native
 * picker on a touch device, so one component serves screens/05's "Both" platform note).
 */
export function FileDropZone({ onFilesChosen, disabled = false }: FileDropZoneProps) {
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [dragActive, setDragActive] = useState(false);

  function handleDrop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragActive(false);
    if (disabled) return;
    onFilesChosen(Array.from(event.dataTransfer.files));
  }

  return (
    <div
      role="button"
      tabIndex={disabled ? -1 : 0}
      aria-disabled={disabled}
      onClick={() => !disabled && inputRef.current?.click()}
      onKeyDown={(e) => {
        if (!disabled && (e.key === "Enter" || e.key === " ")) {
          e.preventDefault();
          inputRef.current?.click();
        }
      }}
      onDragOver={(e) => {
        e.preventDefault();
        if (!disabled) setDragActive(true);
      }}
      onDragLeave={() => setDragActive(false)}
      onDrop={handleDrop}
      className={cn(
        "flex cursor-pointer flex-col items-center justify-center gap-2 rounded-md border-2 border-dashed p-8 text-center transition-colors",
        dragActive ? "border-primary bg-primary-light" : "border-border bg-surface-secondary",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      <UploadCloud size={32} className="text-text-secondary" aria-hidden="true" />
      <p className="text-body text-text-primary">
        <span className="font-medium">Tap to select files</span>
        <span className="hidden sm:inline"> or drag and drop</span>
      </p>
      <p className="text-body-small text-text-secondary">
        CSV, Excel, Parquet, or SQLite
      </p>
      <label htmlFor={inputId} className="sr-only">
        Choose files to upload
      </label>
      <input
        ref={inputRef}
        id={inputId}
        type="file"
        multiple
        disabled={disabled}
        accept={ACCEPTED_UPLOAD_EXTENSIONS.join(",")}
        className="sr-only"
        onChange={(e) => {
          if (e.target.files) onFilesChosen(Array.from(e.target.files));
          e.target.value = "";
        }}
      />
    </div>
  );
}
