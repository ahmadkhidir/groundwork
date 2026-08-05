"use client";

import {
  forwardRef,
  useId,
  useState,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";
import { Eye, EyeOff } from "lucide-react";
import { cn } from "@/lib/cn";

export interface PasswordFieldProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "type"> {
  label: ReactNode;
  error?: string;
  hint?: ReactNode;
}

/**
 * A password field with an inline show/hide toggle, per screens/02-sign-up-login.md's edge case:
 * "Password field visibility toggle (show/hide) available on both platforms."
 *
 * Built alongside `Input` (components/ui/Input.tsx) rather than composed from it: `Input` has no
 * trailing-adornment slot, and reaching for an absolutely-positioned overlay to fake one would mean
 * guessing the input's pixel height instead of using normal flow. This duplicates Input's row
 * structure — worth resolving by adding a trailing-adornment slot to `Input` itself, reported
 * upstream since `components/ui/` is read-only to this slice.
 */
export const PasswordField = forwardRef<HTMLInputElement, PasswordFieldProps>(
  function PasswordField(
    { label, error, hint, className, disabled, ...rest },
    ref,
  ) {
    const [visible, setVisible] = useState(false);
    const inputId = useId();
    const errorId = `${inputId}-error`;
    const hintId = `${inputId}-hint`;
    const hasError = Boolean(error);

    return (
      <div className="flex flex-col gap-xs">
        <label htmlFor={inputId} className="text-body text-text-primary">
          {label}
        </label>

        <div
          className={cn(
            "flex h-(--size-input-height) items-center rounded-md border bg-surface pl-sm",
            hasError ? "border-error" : "border-border",
            !hasError && "focus-within:border-primary",
            disabled && "bg-surface-secondary opacity-50",
          )}
        >
          <input
            ref={ref}
            id={inputId}
            type={visible ? "text" : "password"}
            disabled={disabled}
            aria-invalid={hasError || undefined}
            aria-describedby={hasError ? errorId : hint ? hintId : undefined}
            className={cn(
              "h-full w-full bg-transparent text-body text-text-primary",
              "placeholder:text-text-secondary focus:outline-none",
              className,
            )}
            {...rest}
          />

          <button
            type="button"
            onClick={() => setVisible((current) => !current)}
            disabled={disabled}
            aria-label={visible ? "Hide password" : "Show password"}
            aria-pressed={visible}
            className="touch-target flex shrink-0 items-center justify-center px-sm text-text-secondary hover:text-text-primary disabled:cursor-not-allowed"
          >
            {visible ? (
              <EyeOff size={18} aria-hidden="true" />
            ) : (
              <Eye size={18} aria-hidden="true" />
            )}
          </button>
        </div>

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
  },
);
