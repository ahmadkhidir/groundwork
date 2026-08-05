"use client";

import { useState, type FormEvent } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { AuthBanner } from "./AuthBanner";
import { useOnlineStatus } from "./useOnlineStatus";
import { isValidEmail } from "./validation";
import { api, ApiError } from "@/lib/api-client";

/**
 * Password reset "request" step, reached from the login screen's "Forgot password?" link
 * (screens/02-sign-up-login.md).
 *
 * GAP, reported upstream: no `screens/` file documents this screen (or the confirm step in
 * ResetPasswordForm), and architecture.md wires no email-delivery integration. The Go endpoint this
 * calls (`/v1/auth/password-reset/request`) logs the reset link server-side rather than emailing it
 * — see server/internal/auth/reset_store.go. The response message below describes the intended
 * steady-state behavior (once real delivery lands); it is not yet literally true.
 */
export function ForgotPasswordForm() {
  const online = useOnlineStatus();

  const [email, setEmail] = useState("");
  const [emailError, setEmailError] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitted, setSubmitted] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;

    setBanner(null);
    if (!isValidEmail(email)) {
      setEmailError("Enter a valid email address.");
      return;
    }
    setEmailError(undefined);

    setSubmitting(true);
    try {
      await api.post("/v1/auth/password-reset/request", { email });
      // The endpoint always succeeds this way regardless of whether the account exists — see the
      // handler's doc comment — so the confirmation view below is shown unconditionally too.
      setSubmitted(true);
    } catch (error) {
      if (error instanceof ApiError && error.isOffline) {
        setBanner("Could not reach Groundwork. Check your connection and try again.");
      } else {
        setBanner("Something went wrong. Try again.");
      }
    } finally {
      setSubmitting(false);
    }
  }

  if (submitted) {
    return (
      <div className="flex flex-col gap-4">
        <p className="text-body text-text-primary">
          If an account exists for <strong>{email}</strong>, reset instructions
          have been sent.
        </p>
        <Link href="/login" className="text-body-small text-primary hover:underline">
          Back to log in
        </Link>
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-4">
      {!online && (
        <AuthBanner
          variant="offline"
          message="You're offline — check your connection."
        />
      )}
      {banner && <AuthBanner message={banner} />}

      <p className="text-body-small text-text-secondary">
        Enter the email on your account and we&apos;ll send you instructions to
        reset your password.
      </p>

      <Input
        label="Email"
        type="email"
        autoComplete="email"
        value={email}
        onChange={(event) => setEmail(event.target.value)}
        error={emailError}
        disabled={submitting}
        required
      />

      <Button type="submit" variant="primary" loading={submitting} disabled={!online}>
        Send reset instructions
      </Button>
    </form>
  );
}
