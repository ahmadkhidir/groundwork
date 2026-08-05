import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { signIn } from "next-auth/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LoginForm } from "./LoginForm";

const pushMock = vi.fn();
const refreshMock = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, refresh: refreshMock }),
}));

vi.mock("next-auth/react", () => ({
  signIn: vi.fn(),
}));

const signInMock = vi.mocked(signIn);

async function fillAndSubmit(email: string, password: string) {
  const user = userEvent.setup();
  if (email) await user.type(screen.getByLabelText("Email"), email);
  if (password) await user.type(screen.getByLabelText("Password"), password);
  await user.click(screen.getByRole("button", { name: "Log in" }));
  return user;
}

describe("LoginForm", () => {
  beforeEach(() => {
    pushMock.mockClear();
    refreshMock.mockClear();
    signInMock.mockReset();
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: true,
    });
  });

  // Default state.
  it("renders an empty form on initial load", () => {
    render(<LoginForm />);
    expect(screen.getByLabelText("Email")).toHaveValue("");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Log in" })).not.toBeDisabled();
  });

  // Validation Error state.
  it("shows inline validation errors and does not submit for an invalid email", async () => {
    render(<LoginForm />);
    await fillAndSubmit("not-an-email", "something");

    expect(screen.getByText("Enter a valid email address.")).toBeInTheDocument();
    expect(signInMock).not.toHaveBeenCalled();
  });

  it("shows a validation error for an empty password", async () => {
    render(<LoginForm />);
    await fillAndSubmit("user@example.com", "");

    expect(screen.getByText("Password is required.")).toBeInTheDocument();
    expect(signInMock).not.toHaveBeenCalled();
  });

  // Auth Error state — and the no-enumeration rule: this is the ONLY message the client can show,
  // regardless of which credential was wrong.
  it("shows one generic message when signIn reports an error", async () => {
    signInMock.mockResolvedValue({
      error: "CredentialsSignin",
      status: 401,
      ok: false,
      url: null,
      code: undefined,
    });

    render(<LoginForm />);
    await fillAndSubmit("user@example.com", "wrong-password");

    await waitFor(() => {
      expect(screen.getByText("Incorrect email or password.")).toBeInTheDocument();
    });
    expect(pushMock).not.toHaveBeenCalled();
  });

  // Happy path — redirects to the default destination when no `next` param was supplied.
  it("redirects to the Workspace List on success with no redirectTo", async () => {
    signInMock.mockResolvedValue({
      error: undefined,
      status: 200,
      ok: true,
      url: "/",
      code: undefined,
    });

    render(<LoginForm />);
    await fillAndSubmit("user@example.com", "correct-password");

    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/workspaces"));
  });

  // Authentication Gate: lands back on the originally-requested URL.
  it("redirects to redirectTo on success when it is a safe internal path", async () => {
    signInMock.mockResolvedValue({
      error: undefined,
      status: 200,
      ok: true,
      url: "/",
      code: undefined,
    });

    render(<LoginForm redirectTo="/workspaces/ws_1/reports/rep_1" />);
    await fillAndSubmit("user@example.com", "correct-password");

    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith("/workspaces/ws_1/reports/rep_1"),
    );
  });

  // Open-redirect guard: an external or protocol-relative target must never be honored.
  it("falls back to the Workspace List for an unsafe redirectTo", async () => {
    signInMock.mockResolvedValue({
      error: undefined,
      status: 200,
      ok: true,
      url: "/",
      code: undefined,
    });

    render(<LoginForm redirectTo="//evil.example.com" />);
    await fillAndSubmit("user@example.com", "correct-password");

    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/workspaces"));
  });

  // Offline state.
  it("shows an offline banner and disables submit when the browser is offline", () => {
    Object.defineProperty(window.navigator, "onLine", {
      configurable: true,
      value: false,
    });

    render(<LoginForm />);

    expect(
      screen.getByText("You're offline — check your connection."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Log in" })).toBeDisabled();
  });

  // Rapid double-tap edge case: submitting flips synchronously, so signIn is only ever invoked once
  // for two rapid submissions.
  it("does not double-submit on rapid re-submission", async () => {
    let resolveSignIn: (value: Awaited<ReturnType<typeof signIn>>) => void = () => {};
    signInMock.mockReturnValue(
      new Promise((resolve) => {
        resolveSignIn = resolve;
      }),
    );

    render(<LoginForm />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Email"), "user@example.com");
    await user.type(screen.getByLabelText("Password"), "correct-password");

    const button = screen.getByRole("button", { name: "Log in" });
    await user.click(button);
    await user.click(button); // Second click while the first submission is still pending.

    expect(signInMock).toHaveBeenCalledTimes(1);

    resolveSignIn({ error: undefined, status: 200, ok: true, url: "/", code: undefined });
    await waitFor(() => expect(pushMock).toHaveBeenCalled());
  });
});
