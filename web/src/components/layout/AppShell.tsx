"use client";

import Link from "next/link";
import Image from "next/image";
import type { ReactNode } from "react";
import { NotificationsBell } from "@/components/notifications/NotificationsBell";
import { AccountMenu } from "./AccountMenu";
import { routes } from "@/lib/routes";

export interface AppShellProps {
  displayName: string;
  email: string;
  children: ReactNode;
}

/**
 * The Navigation Shell (screens.md): the sticky Navbar shown on every authenticated screen —
 * brand lockup, the notifications bell, and the account menu (the account-level entry point:
 * Plan & Billing + Profile & Security; sign out deliberately lives only on Profile & Security).
 *
 * The workspace-level Sidebar (Home / Semantic Layer / Settings) and the workspace switcher are
 * the remaining unbuilt pieces of the shell; until they land, workspace screens keep their local
 * contextual nav. The bell previously lived on Workspace Home ("until a Navigation Shell exists") —
 * it now belongs to the shell.
 */
export function AppShell({ displayName, email, children }: AppShellProps) {
  return (
    <div className="flex min-h-full flex-col">
      <header className="sticky top-0 z-40 border-b border-border bg-surface/90 backdrop-blur">
        <nav
          aria-label="Main"
          className="mx-auto flex w-full max-w-6xl items-center justify-between gap-2 px-3 py-2.5 sm:gap-4 sm:px-4 sm:py-3 lg:px-8"
        >
          <Link
            href={routes.workspaces()}
            className="flex min-w-0 items-center gap-1.5 text-body text-text-primary sm:gap-2 sm:text-h3"
          >
            <Image
              src="/images/logo.png"
              alt=""
              width={26}
              height={26}
              className="dark:invert"
              aria-hidden="true"
            />
            <span className="truncate font-semibold">Groundwork</span>
          </Link>

          <div className="flex items-center gap-1 sm:gap-2">
            <NotificationsBell />
            <AccountMenu displayName={displayName} email={email} />
          </div>
        </nav>
      </header>
      {children}
    </div>
  );
}
