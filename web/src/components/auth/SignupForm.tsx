"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { signIn } from "next-auth/react";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { PasswordField } from "./PasswordField";
import { AuthBanner } from "./AuthBanner";
import { useOnlineStatus } from "./useOnlineStatus";
import { isValidEmail } from "./validation";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { User } from "@/types/schema";

interface FieldErrors {
  email?: string;
  password?: string;
}

interface SignupResponse {
  user: User;
}

/**
 * The Sign Up half of screens/02-sign-up-login.md.
 *
 * Two network calls on submit, not one: `POST /v1/auth/signup` creates the account row in Go (the
 * only tier allowed to touch the users table, per architecture.md), then `signIn` establishes the
 * Next.js session by re-running the same credentials through the Credentials provider
 * (web/src/auth.ts), which itself calls `/v1/auth/login`. This is what makes Flow 1 land the user
 * on the Workspace List immediately after signing up, with no separate login step.
 */
export function SignupForm() {
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
    if (submitting) return;

    setAuthError(null);
    if (!validate()) return;

    setSubmitting(true);
    try {
      await api.post<SignupResponse>("/v1/auth/signup", { email, password });

      const result = await signIn("credentials", {
        email,
        password,
        redirect: false,
      });

      if (result?.error) {
        // The account exists but a session couldn't be established. Send the user to log in
        // explicitly rather than claiming a false success (branding.md's honesty-first principle
        // applies to auth state exactly as it does to engine/run status).
        router.push(routes.login());
        return;
      }

      router.push(routes.workspaces());
      router.refresh();
    } catch (error) {
      if (error instanceof ApiError) {
        if (error.isOffline) {
          setAuthError(
            "Could not reach Groundwork. Check your connection and try again.",
          );
        } else if (error.code === "email_taken") {
          setAuthError("An account with that email already exists.");
        } else if (error.code === "invalid_email") {
          setFieldErrors((current) => ({ ...current, email: error.message }));
        } else if (error.code === "validation_error") {
          setFieldErrors((current) => ({
            ...current,
            password: error.message,
          }));
        } else {
          setAuthError(error.message);
        }
      } else {
        setAuthError("Something went wrong. Try again.");
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={handleSubmit} noValidate className="flex flex-col gap-md">
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
        autoComplete="new-password"
        value={password}
        onChange={(event) => setPassword(event.target.value)}
        error={fieldErrors.password}
        disabled={submitting}
        required
      />

      <Button type="submit" variant="primary" loading={submitting} disabled={!online}>
        Sign up
      </Button>
    </form>
  );
}
