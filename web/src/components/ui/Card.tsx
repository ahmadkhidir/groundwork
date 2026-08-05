import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  /** Elevation. Cards default to `sm`; reserve `md` for surfaces that genuinely float (popovers, panels). */
  elevation?: "none" | "sm" | "md";
}

const ELEVATION_CLASSES = {
  none: "shadow-none",
  sm: "shadow-sm",
  md: "shadow-md",
} as const;

/** A surface container: card background, 1px border, `radius-md`, `space-md` padding. */
export function Card({
  elevation = "sm",
  className,
  children,
  ...rest
}: CardProps) {
  return (
    <div
      className={cn(
        "rounded-md border border-border bg-surface p-4",
        ELEVATION_CLASSES[elevation],
        className,
      )}
      {...rest}
    >
      {children}
    </div>
  );
}

export interface CardHeaderProps {
  title: ReactNode;
  /** Secondary line under the title — counts, timestamps, or a one-line explanation. */
  description?: ReactNode;
  /** Trailing slot for actions or a status pill. */
  action?: ReactNode;
  className?: string;
}

/** Standard card header: `h3` title, optional description, optional trailing action. */
export function CardHeader({
  title,
  description,
  action,
  className,
}: CardHeaderProps) {
  return (
    <div className={cn("mb-4 flex items-start justify-between gap-4", className)}>
      <div className="min-w-0">
        <h3 className="text-h3 text-text-primary">{title}</h3>
        {description && (
          <p className="mt-1 text-body-small text-text-secondary">{description}</p>
        )}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}
