"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Button } from "@/components/ui/Button";
import { AuthBanner } from "./AuthBanner";
import { PasswordField } from "./PasswordField";
import { useOnlineStatus } from "./useOnlineStatus";
import { api, ApiError } from "@/lib/api-client";

export interface ResetPasswordFormProps {
  /** The token from the `?token=` query param. Empty when the link is missing one entirely. */
  token: string;
}

/**
 * Password reset "confirm" step. See ForgotPasswordForm's doc comment for the documented gap this
 * flow fills in without a `screens/` spec of its own.
 */
export function ResetPasswordForm({ token }: ResetPasswordFormProps) {
  const router = useRouter();
  const online = useOnlineStatus();

  const [password, setPassword] = useState("");
  const [passwordError, setPasswordError] = useState<string | undefined>();
  const [banner, setBanner] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [succeeded, setSucceeded] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;

    setBanner(null);
    if (password.length === 0) {
      setPasswordError("Password is required.");
      return;
    }
    setPasswordError(undefined);

    setSubmitting(true);
    try {
      await api.post("/v1/auth/password-reset/confirm", {
        token,
        new_password: password,
      });
      setSucceeded(true);
    } catch (error) {
      if (error instanceof ApiError) {
        if (error.isOffline) {
          setBanner("Could not reach Groundwork. Check your connection and try again.");
        } else if (error.code === "invalid_reset_token") {
          setBanner("This reset link is invalid or has expired.");
        } else {
          setBanner(error.message);
        }
      } else {
        setBanner("Something went wrong. Try again.");
      }
    } finally {
      setSubmitting(false);
    }
  }

  if (!token) {
    return (
      <AuthBanner message="This reset link is missing its token. Request a new one." />
    );
  }

  if (succeeded) {
    return (
      <div className="flex flex-col gap-md">
        <p className="text-body text-text-primary">
          Your password has been updated.
        </p>
        <Button variant="primary" onClick={() => router.push("/login")}>
          Log in
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-md">
      {!online && (
        <AuthBanner
          variant="offline"
          message="You're offline — check your connection."
        />
      )}
      {banner && (
        <>
          <AuthBanner message={banner} />
          <Link href="/forgot-password" className="text-body-small text-primary hover:underline">
            Request a new reset link
          </Link>
        </>
      )}

      <PasswordField
        label="New password"
        autoComplete="new-password"
        value={password}
        onChange={(event) => setPassword(event.target.value)}
        error={passwordError}
        disabled={submitting}
        required
      />

      <Button type="submit" variant="primary" loading={submitting} disabled={!online}>
        Reset password
      </Button>
    </form>
  );
}
