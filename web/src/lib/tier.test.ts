import { describe, expect, it } from "vitest";
import { upgradeLabelFor } from "./tier";
import { SubscriptionTier } from "@/types/schema";

describe("upgradeLabelFor", () => {
  it("names Team for a Team-only feature — a Pro user must not be told 'Pro/Team'", () => {
    expect(upgradeLabelFor(SubscriptionTier.Team)).toBe("Upgrade to Team");
  });

  it("keeps the two-plan label for a Pro/Team feature", () => {
    expect(upgradeLabelFor(SubscriptionTier.Pro)).toBe("Upgrade to Pro/Team");
  });

  it("never emits the misleading 'Upgrade to Pro/Team' for a Team-only feature", () => {
    expect(upgradeLabelFor(SubscriptionTier.Team)).not.toContain("Pro");
  });
});
