import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AuthCard } from "./AuthCard";

describe("AuthCard", () => {
  it("renders the logo + wordmark header, the title, the toggle, and children", () => {
    const { container } = render(
      <AuthCard
        title="Log in"
        toggle={{ href: "/signup", prompt: "New to Groundwork?", label: "Create an account" }}
      >
        <p>form body</p>
      </AuthCard>,
    );

    // screens/02's "minimal logo header": the brand lockup sits beside the wordmark above the card.
    const logo = container.querySelector("img");
    expect(logo).not.toBeNull();
    expect(logo).toHaveAttribute("alt", "");
    expect(logo?.getAttribute("src")).toContain("logo.png");
    expect(logo?.className).toContain("dark:invert");
    expect(screen.getByText("Groundwork")).toBeInTheDocument();

    expect(screen.getByRole("heading", { name: "Log in" })).toBeInTheDocument();
    expect(screen.getByText("form body")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an account" })).toHaveAttribute(
      "href",
      "/signup",
    );
  });
});
