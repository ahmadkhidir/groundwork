import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { EngineStatus, RunStatus } from "@/types/schema";
import { StatusPill, engineStatusPill, runStatusPill } from "./StatusPill";

describe("StatusPill", () => {
  it("renders its text label", () => {
    render(<StatusPill variant="success">Ready</StatusPill>);
    expect(screen.getByText("Ready")).toBeInTheDocument();
  });

  /*
   * branding.md §Color independence: status is NEVER conveyed by color alone. The decorative dot is
   * hidden from assistive technology so a screen reader announces the label, not a bare bullet.
   */
  it("hides the decorative dot from assistive technology", () => {
    const { container } = render(
      <StatusPill variant="info" showDot>
        Waking
      </StatusPill>,
    );

    const dot = container.querySelector("[aria-hidden='true']");
    expect(dot).not.toBeNull();
    expect(screen.getByText("Waking")).toBeInTheDocument();
  });

  it("styles variants with theme tokens rather than raw colors", () => {
    const { container } = render(
      <StatusPill variant="warning">Rows excluded</StatusPill>,
    );

    const pill = container.firstElementChild;
    expect(pill?.className).toContain("bg-warning/12");
    expect(pill?.className).toContain("text-warning");
    // A hardcoded hex here would be a Behavior Rule 3 violation.
    expect(pill?.className).not.toMatch(/#[0-9a-f]{3,8}/i);
  });
});

describe("engineStatusPill", () => {
  it.each([
    [EngineStatus.Sleeping, "neutral", "Sleeping"],
    [EngineStatus.Waking, "info", "Waking"],
    [EngineStatus.Ready, "success", "Ready"],
  ])("maps %s to the %s variant", (status, variant, label) => {
    expect(engineStatusPill(status)).toEqual({ variant, label });
  });

  /*
   * Every engine status must map to a labelled pill. A status that fell through would render as an
   * unlabelled indicator, which is exactly the "hidden behind a spinner" failure the product
   * forbids.
   */
  it("covers every engine status in the schema", () => {
    for (const status of Object.values(EngineStatus)) {
      const pill = engineStatusPill(status);
      expect(pill.label.length).toBeGreaterThan(0);
    }
  });
});

describe("runStatusPill", () => {
  it("covers every run status in the schema", () => {
    for (const status of Object.values(RunStatus)) {
      const pill = runStatusPill(status);
      expect(pill.label.length).toBeGreaterThan(0);
    }
  });

  it("distinguishes halted from failed", () => {
    // Halting is a user action with a resumable outcome; failure is not. They must not look alike.
    expect(runStatusPill(RunStatus.Halted).variant).toBe("warning");
    expect(runStatusPill(RunStatus.Failed).variant).toBe("error");
  });
});
