import type { ReactNode } from "react";
import { Check } from "lucide-react";
import { cn } from "@/lib/cn";

/** The three purchasable/current plans, in display order. */
export type PlanId = "free" | "pro" | "team";

export interface PlanInfo {
  id: PlanId;
  name: string;
  /** The headline monthly price, verbatim from monetization.md's Pricing Tiers section. */
  price: string;
  /** The pricing footnote — annual option, per-member pricing, overage caveat. */
  priceNote: string;
  /** One-line positioning copy, drawn from monetization.md's tier target/purpose lines. */
  description: string;
  /** What this tier unlocks, in monetization.md's own gating language. */
  features: string[];
}

/**
 * The plan catalog — the copy source is monetization.md's Pricing Tiers section (docs/Groundwork).
 *
 * Prices, included features, and limits are transcribed from that section verbatim; the feature
 * lines reuse the same gating language the product uses everywhere else (exports, sharing,
 * notifications, column exclusion, follow-up questions, usage view).
 */
export const PLANS: PlanInfo[] = [
  {
    id: "free",
    name: "Free",
    price: "$0",
    priceNote: "per month · pay-as-you-go overage beyond the included allotment",
    description:
      "For occasional individual use — the full core loop at real but low volume, not a crippled trial.",
    features: [
      "1 workspace",
      "Up to 20 questions/runs per month",
      "Full core grounded-answer loop",
      "All verification and trust layers, unrestricted",
      "Full privacy stack",
      "SQL always visible",
      "PDF export",
    ],
  },
  {
    id: "pro",
    name: "Pro",
    price: "$29",
    priceNote: "per month · or $290/year · overage beyond a larger allotment",
    description:
      "For working analysts — higher usage ceilings and the export/workflow features a regular user relies on.",
    features: [
      "Everything in Free, plus:",
      "Up to 5 workspaces",
      "Excel/CSV export",
      "Column-level exclusion",
      "Compositional follow-up questions and fork-on-amendment",
      "Durable notifications",
      "Markdown/HTML, JSON, and single chart image export",
      "Detailed usage view",
      "Read-only report sharing by ID",
    ],
  },
  {
    id: "team",
    name: "Team",
    price: "$79",
    priceNote: "per month · up to 3 members · $15/mo per additional member · pooled allotment",
    description:
      "For teams — shared workspace access and pooled usage across a department, without an enterprise tier.",
    features: [
      "Everything in Pro, plus:",
      "Unlimited workspaces",
      "Read-only workspace sharing",
      "Pooled usage allotment",
      "PowerPoint export",
      "Priority (discounted) overage rates",
    ],
  },
];

export interface PlanCardProps {
  plan: PlanInfo;
  /** True when this card is the user's current plan — renders "Current plan" instead of a CTA. */
  isCurrent: boolean;
  /**
   * The action area's content. The caller decides what belongs here: an Upgrade/Switch button for
   * a non-current paid plan, a "current plan" indicator, or (for the Free card of a paid user) an
   * honest "reverts on cancellation" note — the Free plan has no Stripe price, so it must never
   * render an upgrade action (STRIPE_PRICE_FREE is blank).
   */
  action: ReactNode;
  /** Emphasizes the recommended plan (Pro is monetization.md's "core revenue tier"). */
  highlighted?: boolean;
}

/** One column of the plan comparison — name, price, what the tier unlocks, and its action. */
export function PlanCard({ plan, isCurrent, action, highlighted = false }: PlanCardProps) {
  return (
    <div
      className={cn(
        "flex flex-col rounded-md border bg-surface p-4",
        isCurrent ? "border-primary" : "border-border",
        highlighted && !isCurrent ? "border-primary/60" : null,
      )}
    >
      <div className="mb-3 flex items-start justify-between gap-2">
        <div>
          <h3 className="text-h3 text-text-primary">{plan.name}</h3>
          <p className="mt-1 text-body-small text-text-secondary">{plan.description}</p>
        </div>
        {isCurrent && (
          <span className="shrink-0 rounded-sm bg-primary-light px-2 py-1 text-caption font-medium uppercase tracking-wide text-primary">
            Current plan
          </span>
        )}
      </div>

      <p className="mb-1 text-display text-text-primary">{plan.price}</p>
      <p className="mb-4 text-caption text-text-secondary">{plan.priceNote}</p>

      <ul className="mb-4 flex flex-col gap-1.5">
        {plan.features.map((feature, index) => (
          <li key={feature} className="flex items-start gap-2 text-body-small text-text-primary">
            <Check size={14} className="mt-0.5 shrink-0 text-success" aria-hidden="true" />
            <span className={index === 0 && feature.endsWith(":") ? "font-medium" : undefined}>
              {feature}
            </span>
          </li>
        ))}
      </ul>

      <div className="mt-auto">{action}</div>
    </div>
  );
}

/**
 * Resolves the action label for a target plan against the user's current plan, following the
 * billing pass contract: higher plans are "Upgrade to X", lower paid plans are "Switch to X"
 * (Stripe Checkout handles the proration either way), and the current plan is never a button.
 */
export function planActionLabel(target: PlanId, current: PlanId): string {
  const rank: Record<PlanId, number> = { free: 0, pro: 1, team: 2 };
  if (target === current) return "Current plan";
  return rank[target] > rank[current]
    ? `Upgrade to ${PLANS.find((p) => p.id === target)?.name ?? target}`
    : `Switch to ${PLANS.find((p) => p.id === target)?.name ?? target}`;
}
