import Link from "next/link";
import Image from "next/image";
import { ArrowRight, FileUp, MessageSquareText, ScanSearch } from "lucide-react";
import { buttonClassNames } from "@/components/ui/Button";
import { PLANS } from "@/components/billing/PlanCard";
import { ClaimDemo } from "./ClaimDemo";
import { DeletedAccountBanner } from "./DeletedAccountBanner";
import { routes } from "@/lib/routes";

export interface LandingScreenProps {
  /** True when the visitor has an active session — the primary CTA becomes "Go to Workspaces"
   *  per screens/01's edge case (an authenticated visitor still sees the landing page). */
  signedIn: boolean;
  /** True when redirected here after an account deletion (`?deleted=1`) — shows the confirmation banner. */
  deleted?: boolean;
}

/**
 * Screen 01: Landing Page (docs/Groundwork/screens/01-landing-page.md) — the public marketing
 * surface, server-rendered with no client fetch on load (the one SSR/SSG screen per architecture).
 *
 * Static by design: hero + static Report View mock with provenance-linked claims (ClaimDemo),
 * a 3-step "how it works", a pricing teaser mirroring monetization.md's tiers, and a footer. The
 * only state is the authenticated edge case — a signed-in visitor keeps the page but the primary
 * CTA becomes "Go to Workspaces".
 */
export function LandingScreen({ signedIn, deleted = false }: LandingScreenProps) {
  return (
    <div className="flex min-h-full flex-col">
      {deleted && <DeletedAccountBanner />}
      <header className="sticky top-0 z-40 border-b border-border bg-surface/90 backdrop-blur">
        <nav
          aria-label="Main"
          className="mx-auto flex w-full max-w-6xl items-center justify-between gap-2 px-3 py-2.5 sm:gap-4 sm:px-4 sm:py-3 lg:px-8"
        >
          {/* Brand lockup + wordmark (screens/01's Navbar "logo"). The logo is a dark-on-transparent
              asset, so it inverts in dark mode; it is decorative here because the text beside it is
              the accessible name. Sized down at the mobile base so the two CTAs beside it never jam. */}
          <Link
            href={routes.home()}
            className="flex min-w-0 items-center gap-1.5 text-body text-text-primary sm:gap-2 sm:text-h3"
          >
            <Image
              src="/images/logo.png"
              alt=""
              width={506}
              height={506}
              priority
              className="h-7 w-auto shrink-0 dark:invert sm:h-8"
            />
            <span className="truncate">Groundwork</span>
          </Link>
          <div className="hidden items-center gap-6 text-body-small text-text-secondary sm:flex">
            <a href="#how-it-works" className="hover:text-text-primary">
              How it works
            </a>
            <a href="#pricing" className="hover:text-text-primary">
              Pricing
            </a>
          </div>
          <div className="flex shrink-0 items-center gap-1.5 sm:gap-2">
            <Link
              href={routes.login()}
              className={buttonClassNames("ghost", "h-(--size-button-height) px-2.5 sm:px-3")}
            >
              Log in
            </Link>
            <Link
              href={signedIn ? routes.workspaces() : routes.signup()}
              className={buttonClassNames("primary", "h-(--size-button-height) px-2.5 sm:px-3")}
            >
              {signedIn ? "Go to Workspaces" : "Get started"}
            </Link>
          </div>
        </nav>
      </header>

      <main id="main-content" className="flex-1">
        {/* Hero */}
        <section className="mx-auto w-full max-w-6xl px-4 pb-12 pt-14 text-center lg:px-8 lg:pt-20">
          <p className="mx-auto inline-block rounded-full border border-provenance/40 bg-provenance/10 px-3 py-1 text-caption font-medium uppercase tracking-wide text-provenance">
            Verifiably honest AI analytics
          </p>
          <h1 className="mx-auto mt-4 max-w-3xl text-display text-text-primary">
            Every number, traced back to the data.
          </h1>
          <p className="mx-auto mt-4 max-w-2xl text-body-large text-text-secondary">
            Upload your data, ask questions in plain language, and get AI-narrated answers where
            every figure is provably traced to a real query result — never guessed from context.
          </p>
          <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
            <Link href={signedIn ? routes.workspaces() : routes.signup()} className={buttonClassNames("primary")}>
              {signedIn ? "Go to Workspaces" : "Get started"}
              <ArrowRight size={16} aria-hidden="true" />
            </Link>
            <Link href="#how-it-works" className={buttonClassNames("outline")}>
              See how it works
            </Link>
          </div>

          {/* Static Report View mock with provenance-linked claims (screens/01 example claim). */}
          <div className="mt-12 text-left">
            <ClaimDemo />
          </div>
        </section>

        {/* How it works */}
        <section id="how-it-works" className="border-t border-border bg-surface-secondary/50 py-14">
          <div className="mx-auto w-full max-w-6xl px-4 lg:px-8">
            <h2 className="text-h2 text-text-primary">How it works</h2>
            <p className="mt-1 max-w-2xl text-body text-text-secondary">
              Groundwork separates planning from answering: the model writes SQL against a real
              engine, and narrates only over what that engine actually returned.
            </p>
            <div className="mt-8 grid grid-cols-1 gap-6 md:grid-cols-3">
              <StepCard
                icon={FileUp}
                step="01"
                title="Upload your data"
                body="CSV, Excel, Parquet, or SQLite. Groundwork profiles it and flags sensitive columns before anything else happens."
              />
              <StepCard
                icon={MessageSquareText}
                step="02"
                title="Ask in plain language"
                body="Groundwork writes and runs real SQL against your data — the model never answers from data stuffed into its context."
              />
              <StepCard
                icon={ScanSearch}
                step="03"
                title="Trace every number"
                body="Each figure links to the query and the source rows behind it. Wrongness is visible, never hidden."
              />
            </div>
          </div>
        </section>

        {/* Pricing teaser */}
        <section id="pricing" className="border-t border-border py-14">
          <div className="mx-auto w-full max-w-6xl px-4 lg:px-8">
            <h2 className="text-h2 text-text-primary">Pricing</h2>
            <p className="mt-1 max-w-2xl text-body text-text-secondary">
              Every plan includes the full core loop and every verification and trust layer — tiers
              gate usage volume and convenience, never honesty.
            </p>
            <div className="mt-8 grid grid-cols-1 gap-6 md:grid-cols-3">
              {PLANS.map((plan) => (
                <div
                  key={plan.id}
                  className="flex flex-col rounded-md border border-border bg-surface p-5 shadow-sm"
                >
                  <div className="flex items-start justify-between gap-2">
                    <h3 className="text-h3 text-text-primary">{plan.name}</h3>
                    <p className="text-h3 text-text-primary">{plan.price}</p>
                  </div>
                  <p className="mt-1 text-caption text-text-secondary">{plan.priceNote}</p>
                  <p className="mt-3 flex-1 text-body-small text-text-secondary">
                    {plan.description}
                  </p>
                  <Link
                    href={routes.signup()}
                    className={`mt-4 ${buttonClassNames(
                      plan.id === "pro" ? "primary" : "outline",
                      "w-full",
                    )}`}
                  >
                    Get started
                  </Link>
                </div>
              ))}
            </div>
          </div>
        </section>
      </main>

      <footer className="border-t border-border bg-surface-secondary/50">
        <div className="mx-auto flex w-full max-w-6xl flex-col items-center justify-between gap-4 px-4 py-8 text-body-small text-text-secondary sm:flex-row lg:px-8">
          {/* The brand group wraps on narrow screens: logo + wordmark + tagline would otherwise
              exceed a 320px viewport's content width and overflow the footer. */}
          <div className="flex flex-wrap items-center justify-center gap-x-2 gap-y-1">
            <Image
              src="/images/logo.png"
              alt=""
              width={506}
              height={506}
              className="h-5 w-auto dark:invert"
            />
            <span className="text-text-primary">Groundwork</span>
            <span>· Every number, traced back to the data.</span>
          </div>
          <div className="flex items-center gap-4">
            <Link href={routes.login()} className="hover:text-text-primary">
              Log in
            </Link>
            <Link href={routes.signup()} className="hover:text-text-primary">
              Get started
            </Link>
          </div>
        </div>
      </footer>
    </div>
  );
}

function StepCard({
  icon: Icon,
  step,
  title,
  body,
}: {
  icon: typeof FileUp;
  step: string;
  title: string;
  body: string;
}) {
  return (
    <div className="rounded-md border border-border bg-surface p-5 shadow-sm">
      <div className="flex items-center gap-2">
        <Icon size={18} className="text-provenance" aria-hidden="true" />
        <span className="text-caption font-medium uppercase tracking-wide text-text-secondary">
          Step {step}
        </span>
      </div>
      <h3 className="mt-3 text-h3 text-text-primary">{title}</h3>
      <p className="mt-2 text-body-small text-text-secondary">{body}</p>
    </div>
  );
}
