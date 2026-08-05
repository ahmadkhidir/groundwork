import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * Guards against theme-token collisions in globals.css.
 *
 * WHY THIS EXISTS: Foundation defined semantic spacing tokens (`--spacing-md`, `--spacing-3xl`, …)
 * whose key names are identical to Tailwind's container scale. Tailwind v4 resolves `max-w-*` and
 * `w-*` against the `--spacing-*` namespace, so it emitted:
 *
 *     .max-w-3xl { max-width: var(--spacing-3xl) }   →  64px, not 48rem
 *
 * Every page collapsed into a ~50px sliver. Typecheck, lint, all 63 unit tests, and the production
 * build ALL passed while the app was visually unusable — the failure existed only in generated CSS.
 *
 * These tests encode the rule directly, so the collision cannot be reintroduced silently.
 */

const globalsCss = readFileSync(
  join(process.cwd(), "src/app/globals.css"),
  "utf8",
);

/**
 * Tailwind v4's container scale keys. A `--spacing-*` token sharing any of these names hijacks
 * every `max-w-*` / `w-*` utility that uses it.
 */
const CONTAINER_SCALE_KEYS = [
  "3xs",
  "2xs",
  "xs",
  "sm",
  "md",
  "lg",
  "xl",
  "2xl",
  "3xl",
  "4xl",
  "5xl",
  "6xl",
  "7xl",
];

describe("globals.css theme tokens", () => {
  it("declares no --spacing-* token that shadows a container scale key", () => {
    // Only real declarations, not the explanatory comments naming the hazard.
    const declared = [...globalsCss.matchAll(/^\s*--spacing-([\w-]+)\s*:/gm)].map(
      (match) => match[1],
    );

    const collisions = declared.filter((key) =>
      CONTAINER_SCALE_KEYS.includes(key),
    );

    expect(
      collisions,
      `--spacing-${collisions.join(", --spacing-")} shadow${
        collisions.length === 1 ? "s" : ""
      } Tailwind's container scale. ` +
        "Tailwind will resolve max-w-* against these and collapse page layouts. " +
        "Use the numeric spacing scale instead (p-4 = 16px = space-md).",
    ).toEqual([]);
  });

  it("still defines the color, type, radius, and shadow tokens branding.md requires", () => {
    // A blunt guard that the fix above was not applied by deleting half the theme.
    for (const token of [
      "--color-primary",
      "--color-surface",
      "--color-text-primary",
      "--color-provenance",
      "--color-chart-cat-1",
      "--text-body",
      "--text-display",
      "--radius-md",
      "--shadow-lg",
      "--size-button-height",
    ]) {
      expect(globalsCss).toContain(`${token}:`);
    }
  });

  it("pairs every color token with a dark value", () => {
    // branding.md gives a light AND dark value for every color. Splitting the theme so a token
    // exists in one mode only produces an unreadable surface in the other.
    const darkBlock = globalsCss.slice(globalsCss.indexOf(".dark {"));

    for (const token of [
      "--color-primary",
      "--color-surface",
      "--color-text-primary",
      "--color-border",
      "--color-success",
      "--color-warning",
      "--color-error",
      "--color-info",
      "--color-provenance",
      "--shadow-lg",
    ]) {
      expect(darkBlock, `${token} has no dark value`).toContain(`${token}:`);
    }
  });
});
