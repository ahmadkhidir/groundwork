"use client";

import Link from "next/link";
import Image from "next/image";
import { useEffect, useRef, useState } from "react";
import { usePathname } from "next/navigation";
import { Menu, X } from "lucide-react";
import type { ReactNode } from "react";
import { NotificationsBell } from "@/components/notifications/NotificationsBell";
import { AccountMenu } from "./AccountMenu";
import { WorkspaceSwitcher } from "./WorkspaceSwitcher";
import { WorkspaceSidebar } from "./WorkspaceSidebar";
import { routes } from "@/lib/routes";

export interface AppShellProps {
  displayName: string;
  email: string;
  children: ReactNode;
}

/** Extracts `/workspaces/:id/...` from a pathname; null outside a workspace route. */
function workspaceIdFromPathname(pathname: string): string | null {
  const match = pathname.match(/^\/workspaces\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : null;
}

/**
 * The Navigation Shell (screens.md): the sticky Navbar shown on every authenticated screen —
 * brand lockup, workspace switcher, notifications bell, account menu — plus, within a workspace,
 * the collapsible Sidebar (Home / Semantic Layer / Settings).
 *
 * Mobile: the Navbar keeps the icon-sized bell and account menu (always-accessible; the unread
 * badge must not hide behind a tap), and a hamburger opens the Drawer holding the workspace
 * switcher and the workspace-level Sidebar items — the wide content that would crowd a 390px bar.
 * The Sidebar itself is `hidden lg:flex`.
 *
 * The brand lockup links to the Workspace List (the post-login destination); the switcher is the
 * in-place path between workspaces.
 */
export function AppShell({ displayName, email, children }: AppShellProps) {
  const pathname = usePathname();
  const workspaceId = workspaceIdFromPathname(pathname);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const drawerRef = useRef<HTMLDivElement>(null);

  // Close the drawer when navigation changes the pathname (a drawer link's onClick already closes
  // it; this catches browser back/forward and programmatic pushes). Adjusting state during render
  // is the React-recommended replacement for the setState-in-effect pattern the lint rule rejects:
  // the render is re-run immediately with the drawer closed, so no flash is observable.
  const [lastPathname, setLastPathname] = useState(pathname);
  if (pathname !== lastPathname) {
    setLastPathname(pathname);
    setDrawerOpen(false);
  }

  // Escape closes the drawer; clicking outside it does too.
  useEffect(() => {
    if (!drawerOpen) return;
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setDrawerOpen(false);
    }
    function handlePointerDown(event: MouseEvent) {
      if (!drawerRef.current?.contains(event.target as Node)) setDrawerOpen(false);
    }
    document.addEventListener("keydown", handleKeyDown);
    document.addEventListener("mousedown", handlePointerDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("mousedown", handlePointerDown);
    };
  }, [drawerOpen]);

  const sidebarItems = workspaceId
    ? [
        { href: routes.workspace(workspaceId), label: "Home" },
        { href: routes.semanticLayer(workspaceId), label: "Semantic Layer" },
        { href: routes.settings(workspaceId), label: "Settings" },
      ]
    : [];

  return (
    <div className="flex min-h-full flex-col">
      <header className="sticky top-0 z-40 border-b border-border bg-surface/90 backdrop-blur">
        <nav
          aria-label="Main"
          className="mx-auto flex w-full max-w-6xl items-center justify-between gap-2 px-3 py-2.5 sm:gap-4 sm:px-4 sm:py-3 lg:px-8"
        >
          <div className="flex min-w-0 items-center gap-1.5 sm:gap-2">
            <button
              type="button"
              onClick={() => setDrawerOpen(true)}
              aria-label="Open navigation menu"
              className="touch-target inline-flex h-(--size-button-height) w-(--size-button-height) items-center justify-center rounded-md text-text-secondary hover:text-text-primary lg:hidden"
            >
              <Menu size={20} aria-hidden="true" />
            </button>

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
          </div>

          <div className="flex items-center gap-1 sm:gap-2">
            <div className="hidden lg:block">
              <WorkspaceSwitcher currentWorkspaceId={workspaceId ?? undefined} />
            </div>
            <NotificationsBell />
            <AccountMenu displayName={displayName} email={email} />
          </div>
        </nav>
      </header>

      <div className="flex flex-1 items-stretch">
        {workspaceId && (
          <div className="hidden lg:block">
            <WorkspaceSidebar workspaceId={workspaceId} />
          </div>
        )}
        <div className="min-w-0 flex-1">{children}</div>
      </div>

      {drawerOpen && (
        <div className="fixed inset-0 z-50 lg:hidden" role="dialog" aria-modal="true" aria-label="Navigation menu">
          {/* Backdrop: a plain surface overlay that closes the drawer on outside click. */}
          <div className="absolute inset-0 bg-black/40" aria-hidden="true" />
          <div
            ref={drawerRef}
            className="absolute inset-y-0 left-0 flex w-72 max-w-[85vw] flex-col bg-surface shadow-md"
          >
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <Link
                href={routes.workspaces()}
                className="flex min-w-0 items-center gap-2 text-h3 text-text-primary"
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
              <button
                type="button"
                onClick={() => setDrawerOpen(false)}
                aria-label="Close navigation menu"
                className="touch-target inline-flex h-(--size-button-height) w-(--size-button-height) items-center justify-center rounded-md text-text-secondary hover:text-text-primary"
              >
                <X size={20} aria-hidden="true" />
              </button>
            </div>

            <div className="flex-1 overflow-y-auto p-3">
              <WorkspaceSwitcher currentWorkspaceId={workspaceId ?? undefined} />

              {sidebarItems.length > 0 && (
                <nav aria-label="Workspace navigation" className="mt-4 flex flex-col gap-1">
                  {sidebarItems.map((item) => {
                    const homeHref = workspaceId ? routes.workspace(workspaceId) : null;
                    const active =
                      homeHref !== null &&
                      (item.href === homeHref && pathname === item.href
                        ? true
                        : item.href !== homeHref && pathname.startsWith(item.href));
                    return (
                      <Link
                        key={item.href}
                        href={item.href}
                        onClick={() => setDrawerOpen(false)}
                        aria-current={active ? "page" : undefined}
                        className="flex h-9 items-center rounded-md px-2 text-body-small text-text-secondary hover:bg-surface-secondary hover:text-text-primary"
                      >
                        {item.label}
                      </Link>
                    );
                  })}
                </nav>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
