import { handlers } from "@/auth";

/**
 * Auth.js route handlers.
 *
 * This is the ONLY API route the Next.js tier owns. Every other backend call goes to the Go service
 * through src/lib/api-client.ts — see architecture.md's boundary rule.
 */
export const { GET, POST } = handlers;
