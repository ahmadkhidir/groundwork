import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AccountMenu } from "./AccountMenu";

/**
 * The Navigation Shell's account menu (screens.md): the account-level entry point on every
 * authenticated screen. Per screens.md, the menu items are "Plan & Billing" and "Profile &
 * Security", and sign out is deliberately NOT an item - FEAT-052's single sign-out affordance
 * lives only on the Profile & Security screen.
 */
describe("AccountMenu", () => {
  it("shows the avatar with initials from the display name and opens the menu on click", async () => {
    const user = userEvent.setup();
    render(<AccountMenu displayName="Ada Lovelace" email="ada@example.com" />);

    const trigger = screen.getByRole("button", { name: "Account menu (ada@example.com)" });
    expect(trigger).toHaveTextContent("AL");

    // Menu closed by default.
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();

    await user.click(trigger);
    expect(screen.getByRole("menu", { name: "Account" })).toBeInTheDocument();
    expect(screen.getByText("Ada Lovelace")).toBeInTheDocument();
    expect(screen.getByText("ada@example.com")).toBeInTheDocument();
  });

  it("falls back to the email's first letters when no display name is set", () => {
    render(<AccountMenu displayName="" email="ada@example.com" />);
    expect(screen.getByRole("button", { name: "Account menu (ada@example.com)" })).toHaveTextContent(
      "AD",
    );
  });

  it("links to Plan & Billing and Profile & Security (never a sign-out item)", async () => {
    const user = userEvent.setup();
    render(<AccountMenu displayName="Ada" email="ada@example.com" />);

    await user.click(screen.getByRole("button", { name: "Account menu (ada@example.com)" }));

    expect(screen.getByRole("menuitem", { name: /Plan & Billing/ })).toHaveAttribute(
      "href",
      "/settings",
    );
    expect(screen.getByRole("menuitem", { name: /Profile & Security/ })).toHaveAttribute(
      "href",
      "/settings/profile",
    );
    expect(screen.queryByRole("menuitem", { name: /Sign out/ })).not.toBeInTheDocument();
  });

  it("closes the menu on an outside pointer-down", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <AccountMenu displayName="Ada" email="ada@example.com" />
        <button type="button">Outside</button>
      </div>,
    );

    await user.click(screen.getByRole("button", { name: "Account menu (ada@example.com)" }));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Outside" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("closes the menu on Escape", async () => {
    const user = userEvent.setup();
    render(<AccountMenu displayName="Ada" email="ada@example.com" />);

    await user.click(screen.getByRole("button", { name: "Account menu (ada@example.com)" }));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("closes the menu after choosing an item", async () => {
    const user = userEvent.setup();
    render(<AccountMenu displayName="Ada" email="ada@example.com" />);

    await user.click(screen.getByRole("button", { name: "Account menu (ada@example.com)" }));
    await user.click(screen.getByRole("menuitem", { name: /Plan & Billing/ }));

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
