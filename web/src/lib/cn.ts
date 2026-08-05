import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * Merges class names, resolving Tailwind conflicts so the last value wins.
 *
 * Without this, `cn("p-4", "p-6")` would emit both and let CSS source order decide — meaning a
 * component's own padding could silently beat the override a caller passed in. Use it in every
 * component that accepts a `className` prop.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
