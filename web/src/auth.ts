import NextAuth, { type DefaultSession } from "next-auth";
import Credentials from "next-auth/providers/credentials";
import { routes } from "@/lib/routes";
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
  }
}

/** Shape returned by the Go service's credential-verification endpoint. */
interface LoginResponse {
  user: GroundworkUser;
}

const API_BASE_URL =
  process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

export const { handlers, auth, signIn, signOut } = NextAuth({
  session: { strategy: "jwt" },

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
    /** Copies the app-specific fields onto the token at sign-in. */
    jwt({ token, user }) {
      // `user` is present only on the sign-in pass; on later calls the token already carries these.
      // Auth.js types `user.id` as optional to accommodate adapter flows, hence the explicit guard.
      if (user?.id) {
        token.id = user.id;
        token.tier = user.tier;
      }
      return token;
    },

    /** Projects the token onto the session object components consume. */
    session({ session, token }) {
      session.user.id = token.id;
      session.user.tier = token.tier;
      return session;
    },
  },
});
