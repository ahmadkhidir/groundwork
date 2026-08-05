import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ResetPasswordForm } from "./ResetPasswordForm";
import { ApiError, api } from "@/lib/api-client";

const pushMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
}));

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return { ...actual, api: { ...actual.api, post: vi.fn() } };
});

const postMock = vi.mocked(api.post);

describe("ResetPasswordForm", () => {
  beforeEach(() => {
    pushMock.mockClear();
    postMock.mockReset();
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: true,
    });
  });

  it("shows an error and no form when the link is missing its token", () => {
    render(<ResetPasswordForm token="" />);
    expect(
      screen.getByText("This reset link is missing its token. Request a new one."),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("New password")).not.toBeInTheDocument();
  });

  it("requires a new password before submitting", async () => {
    const user = userEvent.setup();
    render(<ResetPasswordForm token="tok_abc" />);

    await user.click(screen.getByRole("button", { name: "Reset password" }));

    expect(screen.getByText("Password is required.")).toBeInTheDocument();
    expect(postMock).not.toHaveBeenCalled();
  });

  it("submits the token and new password, then shows success", async () => {
    postMock.mockResolvedValue({ message: "Password updated." });

    const user = userEvent.setup();
    render(<ResetPasswordForm token="tok_abc" />);
    await user.type(screen.getByLabelText("New password"), "a-new-password");
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    await waitFor(() => {
      expect(postMock).toHaveBeenCalledWith("/v1/auth/password-reset/confirm", {
        token: "tok_abc",
        new_password: "a-new-password",
      });
    });
    expect(
      await screen.findByText("Your password has been updated."),
    ).toBeInTheDocument();
  });

  it("shows an error for an invalid or expired token", async () => {
    postMock.mockRejectedValue(
      new ApiError(400, "This reset link is invalid or has expired.", "invalid_reset_token"),
    );

    const user = userEvent.setup();
    render(<ResetPasswordForm token="tok_expired" />);
    await user.type(screen.getByLabelText("New password"), "a-new-password");
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    await waitFor(() => {
      expect(
        screen.getByText("This reset link is invalid or has expired."),
      ).toBeInTheDocument();
    });
    expect(
      screen.getByRole("link", { name: "Request a new reset link" }),
    ).toBeInTheDocument();
  });
});
