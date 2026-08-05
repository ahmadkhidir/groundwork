import { cn } from "@/lib/cn";

export interface SkeletonProps {
  className?: string;
}

/** A single pulsing placeholder block, for Loading states while data is in flight. */
export function Skeleton({ className }: SkeletonProps) {
  return (
    <div
      aria-hidden="true"
      className={cn("animate-pulse rounded-md bg-surface-secondary", className)}
    />
  );
}

/** A stack of skeleton cards, sized to roughly match a WorkspaceCard/AuditEntryRow's footprint. */
export function SkeletonList({ count = 3 }: { count?: number }) {
  return (
    <div className="flex flex-col gap-4" role="status" aria-label="Loading">
      {Array.from({ length: count }).map((_, i) => (
        <Skeleton key={i} className="h-20 w-full" />
      ))}
    </div>
  );
}
