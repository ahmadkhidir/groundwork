import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { signIn } from "next-auth/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SignupForm } from "./SignupForm";
import { ApiError, api } from "@/lib/api-client";

const pushMock = vi.fn();
const refreshMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, refresh: refreshMock }),
}));

vi.mock("next-auth/react", () => ({
  signIn: vi.fn(),
}));

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return { ...actual, api: { ...actual.api, post: vi.fn() } };
});

const signInMock = vi.mocked(signIn);
const postMock = vi.mocked(api.post);

async function fillAndSubmit(email: string, password: string) {
  const user = userEvent.setup();
  if (email) await user.type(screen.getByLabelText("Email"), email);
  if (password) await user.type(screen.getByLabelText("Password"), password);
  await user.click(screen.getByRole("button", { name: "Sign up" }));
}

describe("SignupForm", () => {
  beforeEach(() => {
    pushMock.mockClear();
    refreshMock.mockClear();
    signInMock.mockReset();
    postMock.mockReset();
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: true,
    });
  });

  it("shows inline validation errors before hitting the network", async () => {
    render(<SignupForm />);
    await fillAndSubmit("not-an-email", "");

    expect(screen.getByText("Enter a valid email address.")).toBeInTheDocument();
    expect(screen.getByText("Password is required.")).toBeInTheDocument();
    expect(postMock).not.toHaveBeenCalled();
  });

  // Happy path: signup creates the account in Go, then establishes the Next.js session, then lands
  // on the Workspace List — no separate login step, per user_flow.md Flow 1.
  it("creates the account, signs in, and redirects to the Workspace List", async () => {
    postMock.mockResolvedValue({
      user: {
        id: "u_1",
        email: "chidinma@example.com",
        display_name: "Chidinma",
        tier: "free",
        created_at: "2026-08-05T00:00:00Z",
      },
    });
    signInMock.mockResolvedValue({
      error: undefined,
      status: 200,
      ok: true,
      url: "/",
      code: undefined,
    });

    render(<SignupForm />);
    await fillAndSubmit("chidinma@example.com", "a-real-password");

    await waitFor(() => {
      expect(postMock).toHaveBeenCalledWith("/v1/auth/signup", {
        email: "chidinma@example.com",
        password: "a-real-password",
      });
    });
    expect(signInMock).toHaveBeenCalledWith("credentials", {
      email: "chidinma@example.com",
      password: "a-real-password",
      redirect: false,
    });
    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/workspaces"));
  });

  // Auth Error state: "account exists (sign up)" per screens/02-sign-up-login.md.
  it("shows an account-exists error for a duplicate email", async () => {
    postMock.mockRejectedValue(
      new ApiError(409, "An account with that email already exists.", "email_taken"),
    );

    render(<SignupForm />);
    await fillAndSubmit("tunde@example.com", "a-password");

    await waitFor(() => {
      expect(
        screen.getByText("An account with that email already exists."),
      ).toBeInTheDocument();
    });
    expect(signInMock).not.toHaveBeenCalled();
  });

  // Offline state.
  it("shows an offline error when the API is unreachable", async () => {
    postMock.mockRejectedValue(
      new ApiError(0, "Could not reach Groundwork. Check your connection and try again."),
    );

    render(<SignupForm />);
    await fillAndSubmit("ngozi@example.com", "a-password");

    await waitFor(() => {
      expect(
        screen.getByText(
          "Could not reach Groundwork. Check your connection and try again.",
        ),
      ).toBeInTheDocument();
    });
  });

  it("disables the submit button while offline", () => {
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: false,
    });

    render(<SignupForm />);
    expect(screen.getByRole("button", { name: "Sign up" })).toBeDisabled();
  });
});
