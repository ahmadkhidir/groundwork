import type { ReactNode } from "react";
import { AppShell } from "@/components/layout/AppShell";
import { auth } from "@/auth";

/**
 * The authenticated route group layout — wraps every authenticated screen in the Navigation Shell
 * (screens.md). Route groups are URL-invisible: the pages under `(app)/workspaces`, `(app)/settings`,
 * and `(app)/notifications` keep their exact public URLs, so the proxy patterns and PageProps
 * types are unchanged.
 *
 * The shared `[shareToken]` report route stays OUTSIDE the group — it is public (token-possession
 * access) and must not show the authenticated shell.
 */
export default async function AppLayout({ children }: { children: ReactNode }) {
  const session = await auth();

  return (
    <AppShell
      displayName={session?.user?.name ?? ""}
      email={session?.user?.email ?? ""}
    >
      {children}
    </AppShell>
  );
}
