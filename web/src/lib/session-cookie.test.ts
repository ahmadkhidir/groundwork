import { describe, expect, it } from "vitest";
import { sessionTokenCookieConfig } from "./session-cookie";

describe("sessionTokenCookieConfig", () => {
  it("always pins the plain cookie name — a stable decode salt for the bearer transport", () => {
    const config = sessionTokenCookieConfig(undefined);
    expect(config.name).toBe("authjs.session-token");
    expect(config.options.domain).toBeUndefined();
  });

  it("sets the shared parent domain only when COOKIE_DOMAIN is configured", () => {
    expect(sessionTokenCookieConfig(".groundwork.app")).toEqual({
      name: "authjs.session-token",
      options: { domain: ".groundwork.app" },
    });
  });

  it("never uses the __Secure- prefix (it would forbid a Domain attribute)", () => {
    expect(sessionTokenCookieConfig(".groundwork.app").name.startsWith("__Secure-")).toBe(false);
  });
});
