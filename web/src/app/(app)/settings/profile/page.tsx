import type { Metadata } from "next";
import { ProfileScreen } from "@/components/account/ProfileScreen";
import { auth } from "@/auth";

export const metadata: Metadata = { title: "Profile & Security" };

/**
 * Route: `/settings/profile` — FEAT-052's Profile & Security screen (screens/20).
 *
 * Account-level self-management, deliberately distinct from `/workspaces/:id/settings` (the
 * per-workspace settings screen). Protected by the proxy like every authenticated route; the
 * screen's identity comes from the session (the account email and display name are read-only).
 */
export default async function Page() {
  const session = await auth();

  return (
    <ProfileScreen
      email={session?.user?.email ?? ""}
      displayName={session?.user?.name ?? ""}
    />
  );
}
