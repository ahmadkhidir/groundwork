import type { ReactNode } from "react";
import Link from "next/link";
import { Card } from "@/components/ui/Card";

export interface AuthCardProps {
  title: string;
  /**
   * The mode-toggle link required by screens/02-sign-up-login.md ("Mode toggle | Link | Both |
   * Switch between Sign Up / Login").
   *
   * `href` is a literal path rather than a `routes.ts` builder: `/forgot-password` and the signup/
   * login cross-links aren't in the central route map yet (`web/src/lib/` is read-only to this
   * slice) — reported upstream as the exact addition needed.
   */
  toggle: { href: string; prompt: string; label: string };
  children: ReactNode;
}

/**
 * The shared centered-card shell for every auth screen (Sign Up, Login, Forgot Password, Reset
 * Password), per screens/02-sign-up-login.md's layout: "Centered Card ... on a surface-secondary
 * background ... no sidebar/navbar chrome beyond a minimal logo header — deliberately
 * distraction-free," true on both the mobile and web layouts it describes.
 */
export function AuthCard({ title, toggle, children }: AuthCardProps) {
  return (
    <main
      id="main-content"
      className="flex min-h-full flex-1 items-center justify-center bg-surface-secondary p-4"
    >
      <div className="w-full max-w-sm">
        <p className="mb-6 text-center text-h3 text-text-primary">
          Groundwork
        </p>

        <Card>
          <h1 className="mb-6 text-h2 text-text-primary">{title}</h1>

          {children}

          <p className="mt-6 text-center text-body-small text-text-secondary">
            {toggle.prompt}{" "}
            <Link href={toggle.href} className="text-primary hover:underline">
              {toggle.label}
            </Link>
          </p>
        </Card>
      </div>
    </main>
  );
}
