"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { signIn } from "next-auth/react";
import Link from "next/link";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { PasswordField } from "./PasswordField";
import { AuthBanner } from "./AuthBanner";
import { useOnlineStatus } from "./useOnlineStatus";
import { isValidEmail } from "./validation";
import { routes } from "@/lib/routes";

export interface LoginFormProps {
  /**
   * Where to send the user after a successful login — the Authentication Gate's `next` query
   * param (src/proxy.ts / src/lib/routes.ts's `loginWithRedirect`), or undefined for the default
   * destination.
   */
  redirectTo?: string;
}

interface FieldErrors {
  email?: string;
  password?: string;
}

/**
 * Only an internal path is a safe redirect target — passing an unvalidated `next` param straight
 * to `router.push` would be an open redirect. Anything else falls back to the Workspace List, the
 * screen's documented default exit point.
 */
function safeRedirect(target: string | undefined): string {
  if (target && target.startsWith("/") && !target.startsWith("//")) {
    return target;
  }
  return routes.workspaces();
}

/**
 * The Login half of screens/02-sign-up-login.md, implementing every documented state: Default,
 * Submitting, Validation Error, Auth Error, and Offline.
 */
export function LoginForm({ redirectTo }: LoginFormProps) {
  const router = useRouter();
  const online = useOnlineStatus();

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [authError, setAuthError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  function validate(): boolean {
    const errors: FieldErrors = {};
    if (!isValidEmail(email)) errors.email = "Enter a valid email address.";
    if (password.length === 0) errors.password = "Password is required.";
    setFieldErrors(errors);
    return Object.keys(errors).length === 0;
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    // Rapid double-tap: `submitting` is checked and then flipped synchronously, before any await,
    // so a second click landing before the first `setState` re-render still bails out here.
    if (submitting) return;

    setAuthError(null);
    if (!validate()) return;

    setSubmitting(true);
    try {
      const result = await signIn("credentials", {
        email,
        password,
        redirect: false,
      });

      if (result?.error) {
        // web/src/auth.ts's authorize() returns null for BOTH "no such account" and "wrong
        // password" — this one message is all the client can ever show, by design.
        setAuthError("Incorrect email or password.");
        return;
      }

      router.push(safeRedirect(redirectTo));
      router.refresh();
    } catch {
      setAuthError(
        "Could not reach Groundwork. Check your connection and try again.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-4">
      {!online && (
        <AuthBanner
          variant="offline"
          message="You're offline — check your connection."
        />
      )}
      {authError && <AuthBanner message={authError} />}

      <Input
        label="Email"
        type="email"
        autoComplete="email"
        value={email}
        onChange={(event) => setEmail(event.target.value)}
        error={fieldErrors.email}
        disabled={submitting}
        required
      />

      <PasswordField
        label="Password"
        autoComplete="current-password"
        value={password}
        onChange={(event) => setPassword(event.target.value)}
        error={fieldErrors.password}
        disabled={submitting}
        required
      />

      <div className="flex justify-end">
        <Link
          href="/forgot-password"
          className="text-body-small text-primary hover:underline"
        >
          Forgot password?
        </Link>
      </div>

      <Button type="submit" variant="primary" loading={submitting} disabled={!online}>
        Log in
      </Button>
    </form>
  );
}
