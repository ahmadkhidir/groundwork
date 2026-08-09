"use client";

import { useCallback, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { signOut } from "next-auth/react";
import { KeyRound, LogOut, Trash2, UserRound } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { AccountDeleteModal } from "./AccountDeleteModal";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";

export interface ProfileScreenProps {
  /** The signed-in account email (read-only — there is no email-change flow). */
  email: string;
  /** The signed-in display name, as stored per schema.md User.display_name. */
  displayName: string;
}

type PasswordState =
  | { status: "idle" }
  | { status: "submitting" }
  | { status: "updated" }
  | { status: "error"; message: string };

/**
 * Screen 20: Profile & Security (`/settings/profile`) — FEAT-052's account self-management.
 * Three stacked-card sections: Account (identity, read-only), Password (change password), Danger
 * Zone (delete account). Sign out is the product's SINGLE sign-out affordance.
 *
 * A wrong current password returns 400 invalid_current_password from the API and is shown inline —
 * deliberately NOT routed to the login gate (a 401 would be misread as session expiry).
 */
export function ProfileScreen({ email, displayName }: ProfileScreenProps) {
  const router = useRouter();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordState, setPasswordState] = useState<PasswordState>({ status: "idle" });
  const [deleteOpen, setDeleteOpen] = useState(false);

  const submitPasswordChange = useCallback(async () => {
    if (passwordState.status === "submitting") return;
    if (!currentPassword || !newPassword) {
      setPasswordState({ status: "error", message: "Enter your current and new password." });
      return;
    }
    if (newPassword !== confirmPassword) {
      setPasswordState({ status: "error", message: "The new passwords don't match." });
      return;
    }
    setPasswordState({ status: "submitting" });
    try {
      await api.post("/v1/auth/password-change", {
        current_password: currentPassword,
        new_password: newPassword,
      });
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setPasswordState({ status: "updated" });
    } catch (error) {
      setPasswordState({
        status: "error",
        message:
          error instanceof ApiError && error.code === "invalid_current_password"
            ? "Your current password is incorrect."
            : error instanceof ApiError
              ? error.message
              : "Something went wrong. Try again.",
      });
    }
  }, [currentPassword, newPassword, confirmPassword, passwordState.status]);

  const handleAccountDeleted = useCallback(() => {
    // Sign out locally (the session cookie is dead server-side anyway), then land on the Landing
    // Page with the ?deleted=1 confirmation banner.
    void signOut({ redirect: false }).then(() => {
      router.push("/?deleted=1");
    });
  }, [router]);

  return (
    <main id="main-content" className="mx-auto w-full max-w-2xl p-8">
      <nav aria-label="Account navigation" className="mb-4">
        <Link
          href={routes.accountSettings()}
          className="inline-flex items-center gap-1 text-body-small text-text-secondary hover:text-text-primary"
        >
          ← Plan &amp; Billing
        </Link>
      </nav>

      <h1 className="text-h1 text-text-primary">Profile &amp; Security</h1>
      <p className="mt-1 text-body text-text-secondary">
        Manage your account, password, and data.
      </p>

      <div className="mt-6 flex flex-col gap-6">
        {/* Account — identity, read-only. */}
        <Card>
          <CardHeader title="Account" description="Your identity on Groundwork" />
          <dl className="mt-2 flex flex-col gap-3 text-body">
            <div className="flex items-center gap-2">
              <UserRound size={16} className="shrink-0 text-text-secondary" aria-hidden="true" />
              <dt className="w-32 shrink-0 text-text-secondary">Email</dt>
              <dd className="min-w-0 truncate text-text-primary">{email}</dd>
            </div>
            <div className="flex items-center gap-2">
              <UserRound size={16} className="shrink-0 text-text-secondary" aria-hidden="true" />
              <dt className="w-32 shrink-0 text-text-secondary">Display name</dt>
              <dd className="min-w-0 truncate text-text-primary">{displayName}</dd>
            </div>
          </dl>
        </Card>

        {/* Password — change password (authenticated, current password required). */}
        <Card>
          <CardHeader
            title="Password"
            description="Change your password. Existing sessions are not signed out — this only affects the next sign-in."
          />
          <div className="mt-3 flex flex-col gap-3">
            <Input
              type="password"
              label="Current password"
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
              autoComplete="current-password"
              disabled={passwordState.status === "submitting"}
            />
            <Input
              type="password"
              label="New password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              autoComplete="new-password"
              disabled={passwordState.status === "submitting"}
            />
            <Input
              type="password"
              label="Confirm new password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              autoComplete="new-password"
              disabled={passwordState.status === "submitting"}
            />
            {passwordState.status === "updated" && (
              <p role="status" className="text-body-small text-success">
                Password updated.
              </p>
            )}
            {passwordState.status === "error" && (
              <p role="alert" className="text-body-small text-error">
                {passwordState.message}
              </p>
            )}
            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                onClick={() => void submitPasswordChange()}
                loading={passwordState.status === "submitting"}
                disabled={passwordState.status === "submitting"}
              >
                <KeyRound size={16} aria-hidden="true" />
                Update password
              </Button>
            </div>
          </div>
        </Card>

        {/* Danger Zone — delete account. */}
        <Card>
          <CardHeader
            title="Danger Zone"
            description="Deleting your account is permanent and immediate."
          />
          <p className="mt-2 text-body-small text-text-secondary">
            This destroys every workspace you own and all of their data, removes your read-only
            memberships, cancels any paid plan immediately, and deletes your billing details. No
            automatic refund is issued. The deletion cannot be undone.
          </p>
          <div className="mt-4 flex flex-col gap-2">
            <Button variant="outline" onClick={() => void signOut({ redirect: false }).then(() => router.push("/"))}>
              <LogOut size={16} aria-hidden="true" />
              Sign out
            </Button>
            <Button variant="destructive" onClick={() => setDeleteOpen(true)}>
              <Trash2 size={16} aria-hidden="true" />
              Delete account
            </Button>
          </div>
        </Card>
      </div>

      {deleteOpen && (
        <AccountDeleteModal
          email={email}
          onClose={() => setDeleteOpen(false)}
          onDeleted={handleAccountDeleted}
        />
      )}
    </main>
  );
}
