/**
 * Session-cookie configuration for the Groundwork web tier.
 *
 * The name is pinned to the PLAIN `authjs.session-token` in every environment. Two reasons:
 *
 * 1. Cross-origin bearer transport (Vercel web + Fly API): the browser cannot send a cross-origin
 *    cookie to the API, so the web tier's BFF route (app/api/session-token) hands the browser the
 *    raw Auth.js JWE and it arrives at the Go API as `Authorization: Bearer`. The JWE's HKDF salt
 *    is the cookie name it was encoded under, so pinning the name keeps the salt stable across
 *    environments — the Go decoder always uses "authjs.session-token" for a bearer token.
 * 2. The `__Secure-` prefix the HTTPS defaults use would forbid a `Domain` attribute (browsers
 *    reject prefixed domain cookies) if a custom shared-domain setup is later added.
 *
 * The cookie keeps `Secure` + `HttpOnly` + `SameSite=Lax` from the defaults (Auth.js deep-merges
 * this over them). `domain` is set only when COOKIE_DOMAIN is configured — the shared parent
 * domain (leading dot included) for the sibling-subdomain layout (app.groundwork.app web +
 * api.groundwork.app API); local dev / vercel.app setups leave it host-only.
 */
export function sessionTokenCookieConfig(domain: string | undefined): {
  name: string;
  options: { domain?: string };
} {
  return {
    name: "authjs.session-token",
    options: domain ? { domain } : {},
  };
}
