/**
 * Production session-cookie configuration for the Groundwork web tier.
 *
 * The browser calls the Go API service DIRECTLY with the Auth.js session cookie (architecture.md's
 * boundary — the Go side decodes it via internal/authsession, never a proxy), so in production the
 * cookie must travel to a SIBLING subdomain: the web at app.groundwork.app, the API at
 * api.groundwork.app. Auth.js sets host-only cookies by default; a cross-subdomain cookie requires
 * an explicit `Domain` attribute AND the plain cookie name — the `__Secure-` prefix the HTTPS
 * defaults use forbids a Domain attribute, so a prefixed domain cookie is rejected by browsers.
 *
 * Returns undefined (defaults preserved — host-only, secure-prefixed on HTTPS) when no
 * COOKIE_DOMAIN is configured, i.e. local dev, where ports share a host and the cookie works
 * as-is. When COOKIE_DOMAIN is set (the shared parent domain, leading dot included, e.g.
 * ".groundwork.app"), the session token is set under the plain name with that Domain. The Go
 * decoder already checks both names (server/internal/authsession/session.go), so no server change
 * is needed. Auth.js's init deep-merges this over its defaults, preserving httpOnly/sameSite/
 * path/secure.
 */
export function sessionTokenCookieConfig(domain: string | undefined):
  | { name: string; options: { domain: string } }
  | undefined {
  if (!domain) return undefined;
  return { name: "authjs.session-token", options: { domain } };
}
