import { cookies } from "next/headers";

/**
 * BFF route — returns the raw Auth.js session JWE to the browser so it can be sent to the Go API
 * as `Authorization: Bearer` (the web on Vercel and the API on a separate domain share no parent
 * domain, so a cross-origin cookie cannot travel; the API decodes this JWE with the shared
 * AUTH_SECRET — server/internal/authsession).
 *
 * This is the SAME token the httpOnly session cookie holds, read server-side — it does not expose
 * anything a signed-in user could not already obtain from their own session, and it never leaves
 * the browser except as the Authorization header the API already expects. A missing session
 * returns `{ token: null }` (200); the API would reject a stale token on exp anyway.
 */
export const dynamic = "force-dynamic";

export async function GET(): Promise<Response> {
  const store = await cookies();
  const token = store.get("authjs.session-token")?.value ?? null;
  return Response.json({ token });
}
