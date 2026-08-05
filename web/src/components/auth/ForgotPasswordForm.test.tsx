import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ForgotPasswordForm } from "./ForgotPasswordForm";
import { ApiError, api } from "@/lib/api-client";

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return { ...actual, api: { ...actual.api, post: vi.fn() } };
});

const postMock = vi.mocked(api.post);

describe("ForgotPasswordForm", () => {
  beforeEach(() => {
    postMock.mockReset();
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: true,
    });
  });

  it("validates the email before submitting", async () => {
    const user = userEvent.setup();
    render(<ForgotPasswordForm />);

    await user.type(screen.getByLabelText("Email"), "not-an-email");
    await user.click(screen.getByRole("button", { name: "Send reset instructions" }));

    expect(screen.getByText("Enter a valid email address.")).toBeInTheDocument();
    expect(postMock).not.toHaveBeenCalled();
  });

  // The endpoint responds identically whether or not the account exists (no-enumeration rule), and
  // the UI shows the same confirmation regardless.
  it("shows a generic confirmation after a successful request", async () => {
    postMock.mockResolvedValue({
      message: "If an account exists for that email, reset instructions have been sent.",
    });

    const user = userEvent.setup();
    render(<ForgotPasswordForm />);
    await user.type(screen.getByLabelText("Email"), "ngozi@example.com");
    await user.click(screen.getByRole("button", { name: "Send reset instructions" }));

    await waitFor(() => {
      expect(
        screen.getByText(/If an account exists for/),
      ).toBeInTheDocument();
    });
    expect(screen.getByText("ngozi@example.com")).toBeInTheDocument();
  });

  it("shows an offline banner when the API is unreachable", async () => {
    postMock.mockRejectedValue(
      new ApiError(0, "Could not reach Groundwork. Check your connection and try again."),
    );

    const user = userEvent.setup();
    render(<ForgotPasswordForm />);
    await user.type(screen.getByLabelText("Email"), "ngozi@example.com");
    await user.click(screen.getByRole("button", { name: "Send reset instructions" }));

    await waitFor(() => {
      expect(
        screen.getByText(
          "Could not reach Groundwork. Check your connection and try again.",
        ),
      ).toBeInTheDocument();
    });
  });
});
