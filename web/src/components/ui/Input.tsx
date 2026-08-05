import { forwardRef, useId, type InputHTMLAttributes, type ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface InputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "id"> {
  /** Visible label. Required — a placeholder is not a label and disappears on input. */
  label: ReactNode;
  /**
   * Validation error. When present the field is styled with the error border AND the message is
   * rendered as text, so the failure is never communicated by border color alone.
   */
  error?: string;
  /** Helper text shown below the field when there is no error. */
  hint?: ReactNode;
}

/**
 * A labelled text input implementing branding.md's Input state table.
 *
 * Accessibility is wired here so no feature slice has to remember it: the label is bound via a
 * generated id, the error is linked by `aria-describedby`, and `aria-invalid` is set so screen
 * readers announce the failure rather than relying on the red border.
 */
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { label, error, hint, className, disabled, ...rest },
  ref,
) {
  const inputId = useId();
  const errorId = `${inputId}-error`;
  const hintId = `${inputId}-hint`;
  const hasError = Boolean(error);

  return (
    <div className="flex flex-col gap-xs">
      <label htmlFor={inputId} className="text-body text-text-primary">
        {label}
      </label>

      <input
        ref={ref}
        id={inputId}
        disabled={disabled}
        aria-invalid={hasError || undefined}
        aria-describedby={hasError ? errorId : hint ? hintId : undefined}
        className={cn(
          "h-(--size-input-height) w-full rounded-md px-sm",
          "text-body text-text-primary",
          "border bg-surface",
          "placeholder:text-text-secondary",
          hasError ? "border-error" : "border-border",
          // Focus border comes from the token table; the outline ring is global.
          !hasError && "focus:border-primary",
          "disabled:bg-surface-secondary disabled:text-text-secondary disabled:opacity-50",
          className,
        )}
        {...rest}
      />

      {hasError ? (
        <p id={errorId} role="alert" className="text-body-small text-error">
          {error}
        </p>
      ) : hint ? (
        <p id={hintId} className="text-body-small text-text-secondary">
          {hint}
        </p>
      ) : null}
    </div>
  );
});
