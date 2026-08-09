import NextAuth, { type DefaultSession } from "next-auth";
import Credentials from "next-auth/providers/credentials";
import { cookies } from "next/headers";
import type { JWT } from "@auth/core/jwt";
import { routes } from "@/lib/routes";
import { isTierStale, refreshSessionTier } from "@/lib/session-tier";
import { sessionTokenCookieConfig } from "@/lib/session-cookie";
import type { SubscriptionTier, User as GroundworkUser } from "@/types/schema";

/**
 * Authentication for the Next.js tier.
 *
 * BOUNDARY RULE (architecture.md): this layer handles SESSION management only. It does not read or
 * write Postgres, and it does not own the user record. Credentials are verified by the Go API
 * service, which owns the database and re-enforces authorization on every request regardless of
 * what this layer believes. That is why there is no database adapter here and no `pg` dependency —
 * their presence would be the boundary violation, not a convenience.
 *
 * Sessions are JWT-strategy for the same reason: a database session strategy would require this
 * tier to hold a Postgres connection.
 */

/**
 * Extends the session with the Groundwork-specific fields the UI needs on every render.
 *
 * `tier` rides on the session so tier gates can render without an extra round trip. It is a display
 * concern only — the server re-checks entitlement on every gated action, because a JWT claim is
 * user-influenceable data once issued and must never be the sole authority.
 */
declare module "next-auth" {
  interface Session {
    user: {
      id: string;
      tier: SubscriptionTier;
    } & DefaultSession["user"];
  }

  interface User {
    tier: SubscriptionTier;
  }
}

/*
 * Augments the JWT at its source module, `@auth/core/jwt`, rather than the `next-auth/jwt`
 * re-export. The re-export is not resolvable for declaration merging under `moduleResolution:
 * bundler`, so augmenting it silently fails and leaves `token.id` typed as `unknown`.
 */
declare module "@auth/core/jwt" {
  interface JWT {
    id: string;
    tier: SubscriptionTier;
    /** Epoch ms when the tier claim was last read from the Go API. Absent on pre-deploy tokens. */
    tierCheckedAt?: number;
  }
}

/** Shape returned by the Go service's credential-verification endpoint. */
interface LoginResponse {
  user: GroundworkUser;
}

const API_BASE_URL =
  process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

/**
 * Keeps the session's display tier claim honest after server-side plan changes.
 *
 * The claim is set at sign-in from the login response; a Stripe webhook can change the
 * authoritative tier in the Go DB at any later moment (upgrade, downgrade, cancellation), and
 * nothing would update the JWT — so a paid account would keep rendering Free-tier upgrade prompts
 * until the user signed out and back in. This re-reads the authoritative plan on a TTL
 * (SESSION_TIER_REFRESH_MS, see lib/session-tier.ts) and updates the claim in place; @auth/core
 * re-encodes and re-sets the cookie from whatever this returns (session.js).
 *
 * Display-only and fail-soft by design: the Go API re-checks entitlement on every gated action, so
 * a stale or failed refresh is a cosmetic staleness, never a security boundary.
 */
async function refreshTierClaim(token: JWT): Promise<JWT> {
  // Middleware (proxy.ts) runs in the Edge runtime where next/headers has no request scope; the
  // existing claim is correct enough for an optimistic gate there. Server components and route
  // handlers (node runtime, request scope) are where the refresh happens.
  if (process.env.NEXT_RUNTIME === "edge") return token;

  if (!isTierStale(token.tierCheckedAt, Date.now())) return token;

  // Stamp BEFORE the fetch: a slow or unreachable API must not make every request await a network
  // call, and a failed refresh keeps the existing claim for another window.
  token.tierCheckedAt = Date.now();

  try {
    const cookieStore = await cookies();
    const freshTier = await refreshSessionTier(cookieStore.toString());
    if (freshTier !== null) {
      token.tier = freshTier;
    }
  } catch {
    // Never throw from the jwt callback: @auth/core's session action destroys the session cookie
    // when the callback throws (session.js), which would sign the user out over a display-only
    // refresh.
  }

  return token;
}

/**
 * Session-cookie config — see lib/session-cookie.ts. The cookie name is pinned to the plain
 * `authjs.session-token` in every environment (a stable decode salt for the Go API's bearer
 * transport); COOKIE_DOMAIN adds a shared parent domain for a sibling-subdomain layout when set.
 */
const sessionCookieConfig = sessionTokenCookieConfig(process.env.COOKIE_DOMAIN);

export const { handlers, auth, signIn, signOut } = NextAuth({
  session: { strategy: "jwt" },

  // Auth.js v5 requires trusting the host behind a reverse proxy (Vercel terminates TLS and
  // forwards plain HTTP to the function). AUTH_URL is the canonical public origin.
  trustHost: true,

  cookies: { sessionToken: sessionCookieConfig },

  pages: {
    signIn: routes.login(),
    newUser: routes.signup(),
  },

  providers: [
    Credentials({
      name: "Groundwork",
      credentials: {
        email: { label: "Email", type: "email" },
        password: { label: "Password", type: "password" },
      },

      /**
       * Verifies credentials against the Go API.
       *
       * Returns null on any failure — never throws with a reason. Distinguishing "no such account"
       * from "wrong password" here would hand an attacker an account-enumeration oracle; the login
       * screen shows one generic failure message for both.
       */
      async authorize(credentials) {
        const email = credentials?.email;
        const password = credentials?.password;

        if (typeof email !== "string" || typeof password !== "string") {
          return null;
        }

        try {
          const response = await fetch(`${API_BASE_URL}/v1/auth/login`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ email, password }),
          });

          if (!response.ok) return null;

          const { user } = (await response.json()) as LoginResponse;

          return {
            id: user.id,
            email: user.email,
            name: user.display_name,
            tier: user.tier,
          };
        } catch {
          // Network failure reaching the API. The login screen renders its offline state.
          return null;
        }
      },
    }),
  ],

  callbacks: {
    /** Copies the app-specific fields onto the token at sign-in, and keeps the tier claim honest after. */
    jwt: async ({ token, user }) => {
      // `user` is present only on the sign-in pass; on later calls the token already carries these.
      // Auth.js types `user.id` as optional to accommodate adapter flows, hence the explicit guard.
      if (user?.id) {
        token.id = user.id;
        token.tier = user.tier;
        token.tierCheckedAt = Date.now();
        return token;
      }
      return refreshTierClaim(token);
    },

    /** Projects the token onto the session object components consume. */
    session({ session, token }) {
      session.user.id = token.id;
      session.user.tier = token.tier;
      return session;
    },
  },
});
