import { NextResponse } from "next/server";
import { auth } from "@/auth";
import { isPublicRoute, loginWithRedirect, routes } from "@/lib/routes";

/**
 * Request proxy — the authentication gate.
 *
 * NOTE ON THE FILENAME: this is `proxy.ts`, not `middleware.ts`. Next.js 16 renamed the convention;
 * a file named `middleware.ts` is deprecated and will not run. The functionality is unchanged.
 *
 * This is an OPTIMISTIC check only. It redirects an unauthenticated browser away from protected
 * pages so the user sees the login screen rather than an empty shell — it is not the authorization
 * boundary. The Go API re-verifies identity and workspace scope on every single request, because a
 * cookie check at the edge cannot be trusted to protect data.
 *
 * user_flow.md's Authentication Gate requires that a user sent to login lands back on the URL they
 * originally requested, which is what the `next` parameter carries.
 */
export default auth((request) => {
  const { pathname, search } = request.nextUrl;
  const isSignedIn = Boolean(request.auth);

  if (!isSignedIn && !isPublicRoute(pathname)) {
    const intendedPath = `${pathname}${search}`;
    return NextResponse.redirect(
      new URL(loginWithRedirect(intendedPath), request.nextUrl.origin),
    );
  }

  // A signed-in user has no reason to see the auth screens; send them to their workspaces.
  if (isSignedIn && (pathname === routes.login() || pathname === routes.signup())) {
    return NextResponse.redirect(
      new URL(routes.workspaces(), request.nextUrl.origin),
    );
  }

  return NextResponse.next();
});

export const config = {
  /*
   * Skips Next internals, the auth endpoints themselves, and static assets.
   *
   * Excluding `api/auth` is load-bearing: gating the sign-in endpoint behind the sign-in check
   * would make authentication impossible.
   */
  matcher: [
    "/((?!api/auth|_next/static|_next/image|favicon.ico|.*\\.(?:svg|png|jpg|jpeg|gif|webp|ico)$).*)",
  ],
};
