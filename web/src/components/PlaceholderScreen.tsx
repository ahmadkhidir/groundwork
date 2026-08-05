import type { ReactNode } from "react";
import { Card } from "@/components/ui/Card";
import { StatusPill } from "@/components/ui/StatusPill";

export interface PlaceholderScreenProps {
  /** Screen number and name as they appear in docs/Groundwork/screens.md, e.g. "04 — Workspace Home". */
  screen: string;
  /** Path to the screen's spec file, relative to the docs product directory. */
  spec: string;
  /** Feature IDs that will fill this shell, e.g. "FEAT-003". */
  features: string;
  /** Roadmap phase that delivers it. */
  phase: string;
  /** Route parameters resolved for this render, shown so navigation wiring is verifiable by eye. */
  params?: Record<string, string>;
  children?: ReactNode;
}

/**
 * A placeholder shell for a route whose screen has not been implemented yet.
 *
 * The Foundation phase registers every route from screens.md so navigation, layouts, and the auth
 * gate can be verified end to end before any feature work lands. Each feature slice then REPLACES
 * the corresponding placeholder — it does not wrap or extend it.
 *
 * This is deliberately honest rather than a blank page or a fake loading spinner: an unbuilt screen
 * says it is unbuilt, and names the spec and feature that will build it. Anyone clicking through the
 * app can tell what exists from what is only documented.
 */
export function PlaceholderScreen({
  screen,
  spec,
  features,
  phase,
  params,
  children,
}: PlaceholderScreenProps) {
  return (
    <main id="main-content" className="mx-auto w-full max-w-3xl p-xl">
      <Card>
        <div className="flex items-start justify-between gap-md">
          <div className="min-w-0">
            <p className="text-caption uppercase tracking-wide text-text-secondary">
              Screen {screen}
            </p>
            <h1 className="mt-xs text-h1 text-text-primary">Not built yet</h1>
          </div>
          <StatusPill variant="neutral">Placeholder</StatusPill>
        </div>

        <dl className="mt-lg grid grid-cols-[auto_1fr] gap-x-md gap-y-sm text-body-small">
          <dt className="text-text-secondary">Spec</dt>
          <dd className="font-mono text-text-primary">{spec}</dd>

          <dt className="text-text-secondary">Features</dt>
          <dd className="text-text-primary">{features}</dd>

          <dt className="text-text-secondary">Phase</dt>
          <dd className="text-text-primary">{phase}</dd>

          {params &&
            Object.entries(params).map(([key, value]) => (
              <div key={key} className="contents">
                <dt className="text-text-secondary">{key}</dt>
                <dd className="font-mono text-text-primary">{value}</dd>
              </div>
            ))}
        </dl>

        {children && <div className="mt-lg">{children}</div>}
      </Card>
    </main>
  );
}
