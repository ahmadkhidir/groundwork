import { afterEach, describe, expect, it, vi } from "vitest";
import {
  SESSION_TIER_REFRESH_MS,
  isTierStale,
  normalizeSubscriptionTier,
  refreshSessionTier,
} from "./session-tier";

describe("normalizeSubscriptionTier", () => {
  it("maps the Go API's plan strings to the client union", () => {
    expect(normalizeSubscriptionTier("free")).toBe("free");
    expect(normalizeSubscriptionTier("pro")).toBe("pro");
    expect(normalizeSubscriptionTier("team")).toBe("team");
  });

  it("returns null for an unrecognized plan rather than guessing", () => {
    expect(normalizeSubscriptionTier("enterprise")).toBeNull();
    expect(normalizeSubscriptionTier(undefined)).toBeNull();
  });
});

describe("isTierStale", () => {
  const now = 1_000_000_000_000;

  it("treats a claim stamped at sign-in as fresh within the window", () => {
    expect(isTierStale(now, now)).toBe(false);
    expect(isTierStale(now, now + SESSION_TIER_REFRESH_MS - 1)).toBe(false);
  });

  it("refreshes once the window elapses", () => {
    expect(isTierStale(now, now + SESSION_TIER_REFRESH_MS)).toBe(true);
    expect(isTierStale(now, now + SESSION_TIER_REFRESH_MS + 60_000)).toBe(true);
  });

  it("treats a missing stamp (pre-deploy token) as stale so every claim is refreshed once", () => {
    expect(isTierStale(undefined, now)).toBe(true);
  });
});

describe("refreshSessionTier", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns the authoritative plan and forwards the session cookie", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ subscription: { plan: "pro" } }),
    });
    vi.stubGlobal("fetch", fetchMock);

    const tier = await refreshSessionTier("authjs.session-token=abc.def");

    expect(tier).toBe("pro");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/v1/billing/subscription");
    expect((init.headers as Record<string, string>).Cookie).toBe(
      "authjs.session-token=abc.def",
    );
    expect(init.cache).toBe("no-store");
  });

  it("keeps the existing claim when the endpoint rejects (fail-soft)", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 403 }));
    expect(await refreshSessionTier("c=1")).toBeNull();
  });

  it("keeps the existing claim when the API is unreachable (fail-soft)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new TypeError("fetch failed")),
    );
    expect(await refreshSessionTier("c=1")).toBeNull();
  });

  it("keeps the existing claim on an unrecognized plan string", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ subscription: { plan: "lifetime" } }),
      }),
    );
    expect(await refreshSessionTier("c=1")).toBeNull();
  });
});
