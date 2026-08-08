import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AuthCard } from "./AuthCard";

describe("AuthCard", () => {
  it("renders the minimal logo header, the title, the toggle, and children", () => {
    render(
      <AuthCard
        title="Log in"
        toggle={{ href: "/signup", prompt: "New to Groundwork?", label: "Create an account" }}
      >
        <p>form body</p>
      </AuthCard>,
    );

    // screens/02's "minimal logo header": the brand lockup sits above the card.
    const logo = screen.getByRole("img", { name: "Groundwork" });
    // next/image optimizes the src into a /_next/image?url=… query; the underlying asset is the logo.
    expect(logo).toHaveAttribute("src", expect.stringContaining("logo.png"));
    expect(logo.className).toContain("dark:invert");

    expect(screen.getByRole("heading", { name: "Log in" })).toBeInTheDocument();
    expect(screen.getByText("form body")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an account" })).toHaveAttribute(
      "href",
      "/signup",
    );
  });
});
