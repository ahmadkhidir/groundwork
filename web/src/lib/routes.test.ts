import { describe, expect, it } from "vitest";
import { isPublicRoute, loginWithRedirect, routes } from "./routes";

describe("route builders", () => {
  it("builds workspace-scoped paths", () => {
    expect(routes.workspace("ws_1")).toBe("/workspaces/ws_1");
    expect(routes.upload("ws_1")).toBe("/workspaces/ws_1/upload");
    expect(routes.ingestConfirm("ws_1")).toBe("/workspaces/ws_1/ingest/confirm");
    expect(routes.run("ws_1", "run_9")).toBe("/workspaces/ws_1/runs/run_9");
    expect(routes.report("ws_1", "rep_2")).toBe("/workspaces/ws_1/reports/rep_2");
  });

  it("encodes claim ids so a provenance link survives special characters", () => {
    expect(routes.reportClaim("ws_1", "rep_2", "claim/1 2")).toBe(
      "/workspaces/ws_1/reports/rep_2?claim=claim%2F1%202",
    );
  });
});

describe("authentication gate", () => {
  it.each(["/", "/login", "/signup", "/shared/tok_abc123"])(
    "treats %s as public",
    (path) => {
      expect(isPublicRoute(path)).toBe(true);
    },
  );

  it.each([
    "/workspaces",
    "/workspaces/ws_1",
    "/workspaces/ws_1/settings",
    "/notifications",
  ])("treats %s as protected", (path) => {
    expect(isPublicRoute(path)).toBe(false);
  });

  /*
   * A share token grants access to exactly one report. Anything deeper under /shared must not
   * inherit that exemption, or a crafted path could reach a protected surface unauthenticated.
   */
  it("does not extend the public share exemption to nested paths", () => {
    expect(isPublicRoute("/shared/tok_abc/settings")).toBe(false);
  });

  it("preserves the intended destination for post-login redirect", () => {
    // user_flow.md's Authentication Gate: the user lands back where they were headed.
    expect(loginWithRedirect("/workspaces/ws_1/reports/rep_2?claim=c1")).toBe(
      "/login?next=%2Fworkspaces%2Fws_1%2Freports%2Frep_2%3Fclaim%3Dc1",
    );
  });
});
