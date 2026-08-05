import type { Metadata } from "next";
import { AuthCard } from "@/components/auth/AuthCard";
import { ResetPasswordForm } from "@/components/auth/ResetPasswordForm";

export const metadata: Metadata = { title: "Set a new password" };

/**
 * Route: `/reset-password?token=...` — the link an operator relays from the server log written by
 * `POST /v1/auth/password-reset/request` (see server/internal/auth/reset_store.go's doc comment for
 * why there is no real email yet). Same routing gap as `/forgot-password` — see that page's comment.
 */
export default async function Page(props: PageProps<"/reset-password">) {
  const searchParams = await props.searchParams;
  const tokenParam = searchParams.token;
  const token = typeof tokenParam === "string" ? tokenParam : "";

  return (
    <AuthCard
      title="Set a new password"
      toggle={{ href: "/login", prompt: "Remembered it?", label: "Log in" }}
    >
      <ResetPasswordForm token={token} />
    </AuthCard>
  );
}
