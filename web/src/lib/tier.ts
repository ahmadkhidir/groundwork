import { SubscriptionTier } from "@/types/schema";

/**
 * The upgrade CTA label for a locked feature, naming the plan(s) that actually unlock it.
 *
 * A Team-only feature (workspace members, PowerPoint export) must say "Upgrade to Team" — telling a
 * Pro user "Pro/Team" for something Pro will never unlock sends them to the plan screen for
 * nothing. A Pro/Team feature keeps the two-plan label. Free-tier features are never locked, so
 * this is only ever called with Pro or Team (the default branch is unreachable from a locked row).
 */
export function upgradeLabelFor(minTier: SubscriptionTier): string {
  switch (minTier) {
    case SubscriptionTier.Team:
      return "Upgrade to Team";
    case SubscriptionTier.Pro:
      return "Upgrade to Pro/Team";
    default:
      return "Upgrade to Pro/Team";
  }
}
