import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PasswordField } from "./PasswordField";

describe("PasswordField", () => {
  it("masks the value by default", () => {
    render(<PasswordField label="Password" />);
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
  });

  // screens/02-sign-up-login.md edge case: "Password field visibility toggle (show/hide) available
  // on both platforms."
  it("reveals the value when the toggle is clicked, and re-hides it on a second click", async () => {
    const user = userEvent.setup();
    render(<PasswordField label="Password" />);

    const input = screen.getByLabelText("Password");
    const toggle = screen.getByRole("button", { name: "Show password" });

    await user.click(toggle);
    expect(input).toHaveAttribute("type", "text");
    expect(screen.getByRole("button", { name: "Hide password" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Hide password" }));
    expect(input).toHaveAttribute("type", "password");
  });

  it("renders a validation error as text, not color alone", () => {
    render(<PasswordField label="Password" error="Password is required." />);
    expect(screen.getByRole("alert")).toHaveTextContent("Password is required.");
    expect(screen.getByLabelText("Password")).toHaveAttribute("aria-invalid", "true");
  });
});
