/**
 * Central route map.
 *
 * Every internal link and redirect goes through these builders — never a hand-written template
 * string in a component. One place to change a path, and TypeScript catches a missing parameter at
 * the call site instead of producing a 404 at runtime.
 *
 * Derived from docs/Groundwork/screens.md (navigation map) and user_flow.md.
 */

/** Query-parameter names that open an overlay on top of an existing screen rather than navigating. */
export const QueryParam = {
  /**
   * Opens the Provenance Panel over Report View for a given claim id.
   * A URL parameter rather than component state so a provenance view is linkable and survives reload.
   */
  Claim: "claim",
  /** Opens the Export Dialog over Report View. */
  Export: "export",
  /** Post-authentication redirect target, set by the proxy on an unauthenticated request. */
  Next: "next",
} as const;

export const routes = {
  /** Marketing landing page. Public. */
  home: () => "/",

  /* -- Authentication ---------------------------------------------------------------------- */
  signup: () => "/signup",
  login: () => "/login",
  /**
   * Password reset request and confirmation.
   *
   * Both must be reachable while signed OUT — a user who cannot log in is by definition
   * unauthenticated — so they are also registered in PUBLIC_ROUTE_PATTERNS below.
   */
  forgotPassword: () => "/forgot-password",
  resetPassword: () => "/reset-password",

  /* -- Workspaces -------------------------------------------------------------------------- */
  /** Workspace list — the post-login destination. */
  workspaces: () => "/workspaces",
  workspace: (workspaceId: string) => `/workspaces/${workspaceId}`,

  /* -- Ingest ------------------------------------------------------------------------------ */
  upload: (workspaceId: string) => `/workspaces/${workspaceId}/upload`,
  ingestProgress: (workspaceId: string) =>
    `/workspaces/${workspaceId}/ingest/progress`,
  /** Where inferred types, PII flags, and candidate joins are explicitly confirmed before use. */
  ingestConfirm: (workspaceId: string) =>
    `/workspaces/${workspaceId}/ingest/confirm`,

  /* -- Runs & reports ---------------------------------------------------------------------- */
  run: (workspaceId: string, runId: string) =>
    `/workspaces/${workspaceId}/runs/${runId}`,
  report: (workspaceId: string, reportId: string) =>
    `/workspaces/${workspaceId}/reports/${reportId}`,

  /**
   * Report View with the provenance panel open on a specific claim.
   * Kept as a builder so the parameter name lives only in {@link QueryParam}.
   */
  reportClaim: (workspaceId: string, reportId: string, claimId: string) =>
    `/workspaces/${workspaceId}/reports/${reportId}?${QueryParam.Claim}=${encodeURIComponent(claimId)}`,

  /** Report View with the export dialog open. */
  reportExport: (workspaceId: string, reportId: string) =>
    `/workspaces/${workspaceId}/reports/${reportId}?${QueryParam.Export}=1`,

  /** Public, token-scoped read-only view of a shared report. Access is by token possession, not identity. */
  sharedReport: (shareToken: string) => `/shared/${shareToken}`,

  /* -- Workspace management ---------------------------------------------------------------- */
  settings: (workspaceId: string) => `/workspaces/${workspaceId}/settings`,
  auditLog: (workspaceId: string) => `/workspaces/${workspaceId}/audit-log`,
  semanticLayer: (workspaceId: string) =>
    `/workspaces/${workspaceId}/semantic-layer`,
  /** Deletion proof. Survives the workspace itself — reachable after the cascade completes. */
  deletionProof: (workspaceId: string) => `/workspaces/${workspaceId}/deleted`,

  /* -- Account ----------------------------------------------------------------------------- */
  notifications: () => "/notifications",
} as const;

/**
 * Routes reachable without an authenticated session.
 *
 * The proxy (src/proxy.ts) treats everything not matching this list as protected. Adding a public
 * route means adding it here — there is no second list to keep in sync.
 */
export const PUBLIC_ROUTE_PATTERNS: readonly RegExp[] = [
  /^\/$/,
  /^\/login$/,
  /^\/signup$/,
  // A user who needs a password reset cannot be signed in to reach these.
  /^\/forgot-password$/,
  /^\/reset-password$/,
  /^\/shared\/[^/]+$/,
] as const;

/** True when the given pathname may be viewed without signing in. */
export function isPublicRoute(pathname: string): boolean {
  return PUBLIC_ROUTE_PATTERNS.some((pattern) => pattern.test(pathname));
}

/**
 * Builds the login URL carrying a post-login redirect target.
 *
 * user_flow.md's Authentication Gate requires that an unauthenticated user hitting a protected URL
 * lands back on that exact URL after signing in, rather than being dumped on a generic dashboard.
 */
export function loginWithRedirect(intendedPath: string): string {
  return `${routes.login()}?${QueryParam.Next}=${encodeURIComponent(intendedPath)}`;
}
