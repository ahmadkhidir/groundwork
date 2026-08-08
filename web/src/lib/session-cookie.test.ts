import { describe, expect, it } from "vitest";
import { sessionTokenCookieConfig } from "./session-cookie";

describe("sessionTokenCookieConfig", () => {
  it("returns undefined without a COOKIE_DOMAIN — Auth.js defaults (host-only) are preserved", () => {
    expect(sessionTokenCookieConfig(undefined)).toBeUndefined();
    expect(sessionTokenCookieConfig("")).toBeUndefined();
  });

  it("returns the plain cookie name with the shared parent domain when COOKIE_DOMAIN is set", () => {
    expect(sessionTokenCookieConfig(".groundwork.app")).toEqual({
      name: "authjs.session-token",
      options: { domain: ".groundwork.app" },
    });
  });

  it("never uses the __Secure- prefix for a domain cookie (the prefix forbids a Domain attribute)", () => {
    const config = sessionTokenCookieConfig(".groundwork.app");
    expect(config?.name.startsWith("__Secure-")).toBe(false);
    expect(config?.name).toBe("authjs.session-token");
  });
});
