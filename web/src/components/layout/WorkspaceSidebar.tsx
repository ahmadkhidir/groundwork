"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { ChevronLeft, ChevronRight, Home, ListTree, Settings } from "lucide-react";
import { routes } from "@/lib/routes";
import { cn } from "@/lib/cn";

export interface WorkspaceSidebarProps {
  workspaceId: string;
}

/**
 * The Navigation Shell's collapsible workspace Sidebar (screens.md): Home / Semantic Layer /
 * Settings for the current workspace. Shown on desktop beside the content; on mobile the same
 * items move into the shell's Drawer (AppShell) instead of a separate bottom bar.
 *
 * The active item is derived from the current pathname, so every workspace screen highlights its
 * own entry. `aria-current="page"` names the active link for assistive tech.
 */
export function WorkspaceSidebar({ workspaceId }: WorkspaceSidebarProps) {
  const pathname = usePathname();
  const [collapsed, setCollapsed] = useState(false);

  const homeActive = pathname === routes.workspace(workspaceId);
  const semanticActive = pathname.startsWith(routes.semanticLayer(workspaceId));
  const settingsActive = pathname.startsWith(routes.settings(workspaceId));

  const items = [
    { href: routes.workspace(workspaceId), label: "Home", icon: Home, active: homeActive },
    {
      href: routes.semanticLayer(workspaceId),
      label: "Semantic Layer",
      icon: ListTree,
      active: semanticActive,
    },
    { href: routes.settings(workspaceId), label: "Settings", icon: Settings, active: settingsActive },
  ];

  return (
    <aside
      aria-label="Workspace navigation"
      className={cn(
        "sticky top-16 hidden h-[calc(100vh-4rem)] shrink-0 flex-col border-r border-border bg-surface-secondary/50 lg:flex",
        collapsed ? "w-14" : "w-56",
      )}
    >
      <nav className="flex flex-1 flex-col gap-1 p-2">
        {items.map(({ href, label, icon: Icon, active }) => (
          <Link
            key={href}
            href={href}
            aria-current={active ? "page" : undefined}
            title={collapsed ? label : undefined}
            className={cn(
              "flex h-9 items-center gap-2.5 rounded-md px-2 text-body-small text-text-secondary hover:bg-surface-secondary hover:text-text-primary",
              active && "bg-surface-secondary font-medium text-text-primary",
              collapsed && "justify-center px-0",
            )}
          >
            <Icon size={17} className="shrink-0" aria-hidden="true" />
            {!collapsed && <span className="truncate">{label}</span>}
          </Link>
        ))}
      </nav>

      <div className="border-t border-border p-2">
        <button
          type="button"
          onClick={() => setCollapsed((value) => !value)}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          className="flex h-9 w-full items-center justify-center gap-2 rounded-md text-caption text-text-secondary hover:bg-surface-secondary hover:text-text-primary"
        >
          {collapsed ? (
            <ChevronRight size={16} aria-hidden="true" />
          ) : (
            <>
              <ChevronLeft size={16} aria-hidden="true" />
              <span>Collapse</span>
            </>
          )}
        </button>
      </div>
    </aside>
  );
}
