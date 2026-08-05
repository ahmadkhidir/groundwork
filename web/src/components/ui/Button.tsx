import { forwardRef, type ButtonHTMLAttributes } from "react";
import { cn } from "@/lib/cn";

/**
 * Button variants, one per row of branding.md's Button component-token table.
 *
 * `destructive` exists specifically for the deletion cascade confirmation — the one place in the
 * product where an irreversible action is offered — and should not be reached for merely to signal
 * emphasis.
 */
export type ButtonVariant =
  | "primary"
  | "secondary"
  | "outline"
  | "ghost"
  | "destructive";

/**
 * Classes per variant. Every color is a theme token; no raw hex appears here or at any call site.
 *
 * White-on-primary and white-on-error text is written as `text-white` because branding.md specifies
 * literal #FFFFFF for those two rows — it is a fixed foreground for a colored fill, not the
 * theme-dependent `surface` token, and must not flip in dark mode.
 */
const VARIANT_CLASSES: Record<ButtonVariant, string> = {
  primary: "bg-primary text-white hover:bg-primary-hover",
  secondary: "bg-primary-light text-primary hover:bg-primary-light/80",
  outline:
    "bg-transparent text-text-primary border border-border hover:bg-surface-secondary",
  ghost: "bg-transparent text-text-secondary hover:bg-surface-secondary",
  destructive: "bg-error text-white hover:bg-error/90",
};

/** Shape, size, and typography shared by every variant. */
const BASE_CLASSES = [
  "inline-flex items-center justify-center gap-xs",
  "h-(--size-button-height) rounded-md px-md py-[10px]",
  "text-body font-medium whitespace-nowrap",
  "transition-colors duration-150",
  "disabled:cursor-not-allowed disabled:opacity-50",
].join(" ");

/**
 * Button styling as a class string, for elements that must not be a `<button>`.
 *
 * A navigation action has to render as an anchor so it keeps middle-click, "open in new tab", and
 * the correct screen-reader role. Such a link uses this helper rather than copying the classes,
 * which keeps one definition of what a button looks like.
 */
export function buttonClassNames(
  variant: ButtonVariant = "primary",
  className?: string,
): string {
  return cn(BASE_CLASSES, VARIANT_CLASSES[variant], className);
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  /**
   * Renders a busy state: disables interaction and announces via `aria-busy`.
   *
   * The label stays visible rather than being swapped for a bare spinner — status is never hidden,
   * and a user must always be able to tell what the pending action was.
   */
  loading?: boolean;
  /** Applies the 44px touch-target floor. Set on icon-only and compact controls. */
  touchTarget?: boolean;
}

/**
 * The standard button.
 *
 * Height, radius, and padding come from branding.md's token table; the focus ring is inherited from
 * the global `:focus-visible` rule in globals.css rather than being redeclared per component.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant = "primary",
      loading = false,
      touchTarget = false,
      disabled,
      className,
      children,
      type = "button",
      ...rest
    },
    ref,
  ) {
    return (
      <button
        ref={ref}
        // Defaults to "button": an unspecified type inside a form submits it, which is a
        // long-standing source of accidental submissions.
        type={type}
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        className={buttonClassNames(
          variant,
          cn(touchTarget && "touch-target", className),
        )}
        {...rest}
      >
        {children}
      </button>
    );
  },
);
