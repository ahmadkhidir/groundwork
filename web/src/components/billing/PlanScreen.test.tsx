import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PlanScreen } from "./PlanScreen";
import { ApiError, api } from "@/lib/api-client";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, get: vi.fn(), post: vi.fn() },
  };
});

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);

// A reactive URLSearchParams store so tests can simulate real navigation: when `router.replace`
// drops the upgrade param, consumers re-render — exactly like Next's useSearchParams does after a
// navigation (same pattern as ExportDialog.test.tsx).
const { replaceMock, searchParamsStore } = vi.hoisted(() => {
  let value = new URLSearchParams();
  const listeners = new Set<() => void>();
  const store = {
    get: () => value,
    set: (next: URLSearchParams) => {
      value = next;
      listeners.forEach((listener) => listener());
    },
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
  return {
    replaceMock: vi.fn((url: string) => {
      const q = url.includes("?") ? url.slice(url.indexOf("?") + 1) : "";
      store.set(new URLSearchParams(q));
    }),
    searchParamsStore: store,
  };
});
vi.mock("next/navigation", async () => {
  const React = await import("react");
  return {
    useRouter: () => ({ push: vi.fn(), replace: replaceMock }),
    useSearchParams: () =>
      React.useSyncExternalStore(searchParamsStore.subscribe, searchParamsStore.get),
  };
});

const assignMock = vi.fn();

/** GET /v1/billing/subscription payload, defaulting to a Free user with no Stripe subscription. */
function subscriptionResponse(
  overrides: Partial<{
    plan: string;
    stripe_subscription_exists: boolean;
    stripe_subscription_status: string;
    stripe_subscription_period_end: string;
  }> = {},
) {
  return {
    subscription: {
      plan: "free",
      stripe_subscription_exists: false,
      stripe_subscription_status: "",
      stripe_subscription_period_end: "",
      ...overrides,
    },
  };
}

function renderScreen() {
  return render(<PlanScreen />);
}

describe("PlanScreen", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
    replaceMock.mockClear();
    assignMock.mockReset();
    searchParamsStore.set(new URLSearchParams());

    // jsdom's location.assign is a no-op that throws; replace it with a spy while keeping the
    // real pathname/href so URL-cleanup effects keep working.
    vi.stubGlobal("location", { ...window.location, pathname: "/settings", assign: assignMock });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows a loading skeleton, then the current plan and the full plan comparison", async () => {
    getMock.mockResolvedValue(subscriptionResponse());

    renderScreen();

    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
    expect(getMock).toHaveBeenCalledWith("/v1/billing/subscription");

    await waitFor(() => expect(screen.getByText("Compare plans")).toBeInTheDocument());

    // Current-plan card + comparison, with monetization.md's headline prices.
    expect(screen.getAllByText("Current plan").length).toBeGreaterThan(0);
    expect(screen.getByRole("heading", { name: "Free" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Pro" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Team" })).toBeInTheDocument();
    expect(screen.getByText("$0")).toBeInTheDocument();
    expect(screen.getByText("$29")).toBeInTheDocument();
    expect(screen.getByText("$79")).toBeInTheDocument();
  });

  it("shows Current plan on the Free tier and Upgrade actions on Pro and Team", async () => {
    getMock.mockResolvedValue(subscriptionResponse());

    renderScreen();

    await waitFor(() => expect(screen.getByText("Compare plans")).toBeInTheDocument());

    expect(screen.getByRole("button", { name: "Current plan" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Upgrade to Pro" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upgrade to Team" })).toBeInTheDocument();
  });

  it("POSTs /v1/billing/checkout and redirects the browser to Stripe hosted Checkout", async () => {
    getMock.mockResolvedValue(subscriptionResponse());
    postMock.mockResolvedValue({
      checkout: { session_id: "cs_123", url: "https://checkout.stripe.com/c/pay/cs_123" },
    });
    const user = userEvent.setup();

    renderScreen();
    await user.click(await screen.findByRole("button", { name: "Upgrade to Pro" }));

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith("/v1/billing/checkout", { plan: "pro" }),
    );
    // Hosted Checkout: a full-browser redirect, never an inline render.
    await waitFor(() =>
      expect(assignMock).toHaveBeenCalledWith("https://checkout.stripe.com/c/pay/cs_123"),
    );
  });

  it("shows the honest success banner from ?upgrade=success and cleans the param from the URL", async () => {
    getMock.mockResolvedValue(subscriptionResponse());
    searchParamsStore.set(new URLSearchParams("upgrade=success"));

    renderScreen();

    // The banner says "being updated", not that the new tier is instantly active — the webhook
    // tier change can lag the Stripe redirect (honest-state principle).
    await waitFor(() =>
      expect(screen.getByText(/Your plan is being updated/)).toBeInTheDocument(),
    );
    await waitFor(() => expect(replaceMock).toHaveBeenCalled());
    const replacedUrl = replaceMock.mock.calls[0][0] as string;
    expect(replacedUrl).not.toContain("upgrade");
  });

  it("shows the cancelled note from ?upgrade=cancelled and cleans the param", async () => {
    getMock.mockResolvedValue(subscriptionResponse());
    searchParamsStore.set(new URLSearchParams("upgrade=cancelled"));

    renderScreen();

    await waitFor(() =>
      expect(screen.getByText(/Checkout was cancelled — your plan hasn't changed/)).toBeInTheDocument(),
    );
    await waitFor(() => expect(replaceMock).toHaveBeenCalled());
    const replacedUrl = replaceMock.mock.calls[0][0] as string;
    expect(replacedUrl).not.toContain("upgrade");
  });

  it("shows the shared error state with retry when the subscription read fails", async () => {
    getMock.mockRejectedValueOnce(
      new ApiError(0, "Could not reach Groundwork. Check your connection and try again."),
    );
    const user = userEvent.setup();

    renderScreen();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Try Again" })).toBeInTheDocument(),
    );

    getMock.mockResolvedValueOnce(subscriptionResponse());
    await user.click(screen.getByRole("button", { name: "Try Again" }));

    await waitFor(() => expect(screen.getByText("Compare plans")).toBeInTheDocument());
  });

  it("shows the honest billing_not_configured state when checkout is attempted without Stripe keys", async () => {
    getMock.mockResolvedValue(subscriptionResponse());
    postMock.mockRejectedValue(
      new ApiError(500, "Billing is not configured for that plan yet.", "billing_not_configured"),
    );
    const user = userEvent.setup();

    renderScreen();
    await user.click(await screen.findByRole("button", { name: "Upgrade to Pro" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        /Billing isn't configured for this plan yet/,
      ),
    );
    // The button returns to idle so the user can retry once keys exist.
    expect(screen.getByRole("button", { name: "Upgrade to Pro" })).toBeInTheDocument();
  });

  it("shows the subscription status, period end, and an honest cancel note for a paid user", async () => {
    getMock.mockResolvedValue(
      subscriptionResponse({
        plan: "pro",
        stripe_subscription_exists: true,
        stripe_subscription_status: "active",
        stripe_subscription_period_end: "2026-09-01T00:00:00Z",
      }),
    );

    renderScreen();

    await waitFor(() => expect(screen.getByText("Active")).toBeInTheDocument());
    expect(screen.getByText(/Current period ends/)).toBeInTheDocument();

    // No cancel endpoint exists server-side (only checkout + subscription read + webhook), so the
    // screen says the truth: managed through Stripe — never a fabricated cancel button.
    expect(
      screen.getByText(/Cancelling or downgrading is managed through Stripe/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /cancel/i })).not.toBeInTheDocument();
  });

  it("offers Switch to Pro for a Team user and never an upgrade action on the Free card", async () => {
    getMock.mockResolvedValue(
      subscriptionResponse({
        plan: "team",
        stripe_subscription_exists: true,
        stripe_subscription_status: "active",
        stripe_subscription_period_end: "2026-09-01T00:00:00Z",
      }),
    );

    renderScreen();

    await waitFor(() => expect(screen.getByText("Compare plans")).toBeInTheDocument());

    expect(screen.getByRole("button", { name: "Switch to Pro" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Upgrade to Team" })).not.toBeInTheDocument();
    // The Free plan has no Stripe price — never an upgrade target, even for a paid user. The path
    // back to Free is cancellation, which the screen's honest note covers.
    expect(screen.queryByRole("button", { name: /Free/i })).not.toBeInTheDocument();
    expect(screen.getByText(/Reverts when a paid plan is cancelled/)).toBeInTheDocument();
  });

  it("keeps the other plan cards interactive after a checkout error (retry path)", async () => {
    getMock.mockResolvedValue(subscriptionResponse());
    postMock.mockRejectedValueOnce(
      new ApiError(502, "The payment provider could not start checkout right now.", "payment_provider_error"),
    );
    postMock.mockResolvedValueOnce({
      checkout: { session_id: "cs_2", url: "https://checkout.stripe.com/c/pay/cs_2" },
    });
    const user = userEvent.setup();

    renderScreen();
    await user.click(await screen.findByRole("button", { name: "Upgrade to Team" }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(/payment provider/i),
    );

    // Retry the same action succeeds and redirects.
    await user.click(screen.getByRole("button", { name: "Upgrade to Team" }));
    await waitFor(() =>
      expect(assignMock).toHaveBeenCalledWith("https://checkout.stripe.com/c/pay/cs_2"),
    );
  });
});
