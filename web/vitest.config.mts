import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

/**
 * Vitest configuration.
 *
 * `.mts` because the config uses ESM syntax and Vite's native config loader (the coming default)
 * would otherwise load it as CommonJS.
 *
 * `resolve.tsconfigPaths` is Vite's built-in replacement for vite-tsconfig-paths; it makes the
 * `@/*` alias resolve in tests exactly as it does in the app, so a test never imports through a
 * different path than production code does.
 */
export default defineConfig({
  plugins: [react()],
  resolve: { tsconfigPaths: true },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
