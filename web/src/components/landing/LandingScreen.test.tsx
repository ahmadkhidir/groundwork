import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { LandingScreen } from "./LandingScreen";

describe("LandingScreen", () => {
  it("renders the hero, how-it-works, pricing teaser, and footer for a visitor", () => {
    render(<LandingScreen signedIn={false} />);

    // The brand lockup sits in the navbar and the footer (screens/01's Navbar "logo").
    const logos = screen.getAllByRole("img", { name: "Groundwork" });
    expect(logos.length).toBeGreaterThanOrEqual(2);
    for (const logo of logos) {
      expect(logo.className).toContain("dark:invert");
    }

    expect(
      screen.getByRole("heading", { name: "Every number, traced back to the data." }),
    ).toBeInTheDocument();
    // "How it works" / "Pricing" appear in the nav AND as section headings.
    expect(screen.getAllByText("How it works").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Pricing").length).toBeGreaterThan(0);

    // How-it-works: three steps.
    expect(screen.getByText("Step 01")).toBeInTheDocument();
    expect(screen.getByText("Upload your data")).toBeInTheDocument();
    expect(screen.getByText("Step 02")).toBeInTheDocument();
    expect(screen.getByText("Ask in plain language")).toBeInTheDocument();
    expect(screen.getByText("Step 03")).toBeInTheDocument();
    expect(screen.getByText("Trace every number")).toBeInTheDocument();

    // Pricing teaser mirrors monetization.md's three tiers.
    expect(screen.getByRole("heading", { name: "Free" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Pro" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Team" })).toBeInTheDocument();
    expect(screen.getByText("$0")).toBeInTheDocument();
    expect(screen.getByText("$29")).toBeInTheDocument();
    expect(screen.getByText("$79")).toBeInTheDocument();
  });

  it("swaps the primary CTA from signup to workspaces for an authenticated visitor", () => {
    const { rerender } = render(<LandingScreen signedIn={false} />);
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Get started" })).toHaveAttribute(
      "href",
      "/signup",
    );

    rerender(<LandingScreen signedIn={true} />);
    // The navbar and hero CTAs become "Go to Workspaces"; the static marketing links (footer,
    // pricing cards) correctly keep pointing at signup.
    const goToWorkspaces = screen.getAllByRole("link", { name: /Go to Workspaces/ });
    expect(goToWorkspaces.length).toBeGreaterThan(0);
    for (const link of goToWorkspaces) {
      expect(link).toHaveAttribute("href", "/workspaces");
    }
    expect(within(nav).queryByRole("link", { name: "Get started" })).not.toBeInTheDocument();
  });

  it("links the pricing teaser cards and login entry point correctly", () => {
    render(<LandingScreen signedIn={false} />);
    const logIn = screen.getAllByRole("link", { name: "Log in" });
    expect(logIn.length).toBeGreaterThan(0);
    for (const link of logIn) {
      expect(link).toHaveAttribute("href", "/login");
    }
    const pricingSection = screen
      .getByRole("heading", { name: "Pricing" })
      .closest("section#pricing");
    expect(pricingSection).not.toBeNull();
    const pricingGetStarted = within(pricingSection as HTMLElement).getAllByRole("link", {
      name: "Get started",
    });
    expect(pricingGetStarted).toHaveLength(3);
    for (const link of pricingGetStarted) {
      expect(link).toHaveAttribute("href", "/signup");
    }
  });

  it("demonstrates the provenance mechanism statically: clicking a figure reveals its source", async () => {
    const user = userEvent.setup();
    render(<LandingScreen signedIn={false} />);

    // The demo narrative renders with provenance-linked numbers.
    expect(screen.getByText(/3,420 customers/)).toBeInTheDocument();

    // Clicking the figure reveals the static provenance panel with the source query.
    await user.click(screen.getByRole("button", { name: /3,420 — 3,420 customers/ }));
    await waitFor(() =>
      expect(screen.getByRole("region", { name: /Provenance for 3,420/ })).toBeInTheDocument(),
    );
    expect(screen.getByText("Source query")).toBeInTheDocument();
    expect(screen.getByText(/COUNT\(DISTINCT customer_id\)/)).toBeInTheDocument();
  });
});
