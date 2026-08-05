import Link from "next/link";
import { buttonClassNames } from "@/components/ui/Button";
import { routes } from "@/lib/routes";

/**
 * Landing page — screen 01, see docs/Groundwork/screens/01-landing-page.md.
 *
 * Foundation renders the tagline and the two entry points into the auth flow, so the public shell
 * and the auth gate are verifiable end to end before feature work lands. The full marketing content
 * is FEAT-044, Phase 1.
 */
export default function LandingPage() {
  return (
    <main
      id="main-content"
      className="mx-auto flex w-full max-w-3xl flex-1 flex-col items-center justify-center gap-lg p-xl text-center"
    >
      <div className="flex flex-col gap-md">
        <h1 className="text-display text-text-primary">Groundwork</h1>
        <p className="text-body-large text-text-secondary">
          Every number, traced back to the data.
        </p>
      </div>

      <div className="flex flex-wrap items-center justify-center gap-sm">
        {/*
          Rendered as links, not buttons: these navigate, so they must keep middle-click,
          open-in-new-tab, and the anchor role. buttonClassNames gives them the button's appearance
          without duplicating its tokens.
        */}
        <Link href={routes.signup()} className={buttonClassNames("primary")}>
          Get started
        </Link>
        <Link href={routes.login()} className={buttonClassNames("outline")}>
          Log in
        </Link>
      </div>

      <p className="text-body-small text-text-secondary">
        Screen 01 · <span className="font-mono">screens/01-landing-page.md</span>{" "}
        · FEAT-044 · Phase 1
      </p>
    </main>
  );
}
