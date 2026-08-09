import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { DeletedAccountBanner } from "./DeletedAccountBanner";

describe("DeletedAccountBanner", () => {
  it("shows the neutral post-deletion confirmation", () => {
    render(<DeletedAccountBanner />);
    expect(screen.getByText("Your account has been deleted. All of your data is gone.")).toBeInTheDocument();
  });

  it("dismisses", async () => {
    const user = userEvent.setup();
    const { container } = render(<DeletedAccountBanner />);
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(container.firstChild).toBeNull();
  });
});
