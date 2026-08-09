import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProfileScreen } from "./ProfileScreen";
import { ApiError, api } from "@/lib/api-client";

vi.mock("next-auth/react", () => ({
  signOut: vi.fn(() => Promise.resolve()),
}));

const { pushMock, routerMock } = vi.hoisted(() => {
  const pushMock = vi.fn();
  return { pushMock, routerMock: { push: pushMock } };
});
vi.mock("next/navigation", () => ({
  useRouter: () => routerMock,
}));

vi.mock("@/lib/api-client", async () => {
  const actual =
    await vi.importActual<typeof import("@/lib/api-client")>("@/lib/api-client");
  return {
    ...actual,
    api: { ...actual.api, post: vi.fn(), delete: vi.fn() },
  };
});

const postMock = vi.mocked(api.post);
const deleteMock = vi.mocked(api.delete);

const { signOut } = await import("next-auth/react");
const signOutMock = vi.mocked(signOut);

describe("ProfileScreen", () => {
  beforeEach(() => {
    postMock.mockReset();
    deleteMock.mockReset();
    pushMock.mockReset();
    signOutMock.mockClear();
  });

  const renderScreen = () =>
    render(
      <ProfileScreen email="user@example.com" displayName="User" />,
    );

  it("shows the read-only account identity and the three sections", () => {
    renderScreen();
    expect(screen.getByText("user@example.com")).toBeInTheDocument();
    expect(screen.getByText("User")).toBeInTheDocument();
    expect(screen.getByText("Password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Delete account/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Sign out/ })).toBeInTheDocument();
  });

  it("changes the password and shows the inline confirmation", async () => {
    postMock.mockResolvedValue({ updated: true });
    const user = userEvent.setup();
    renderScreen();

    await user.type(screen.getByLabelText("Current password"), "old-pass-1");
    await user.type(screen.getByLabelText("New password"), "new-pass-2");
    await user.type(screen.getByLabelText("Confirm new password"), "new-pass-2");
    await user.click(screen.getByRole("button", { name: /Update password/ }));

    expect(postMock).toHaveBeenCalledWith("/v1/auth/password-change", {
      current_password: "old-pass-1",
      new_password: "new-pass-2",
    });
    expect(await screen.findByText("Password updated.")).toBeInTheDocument();
  });

  it("shows an inline error for a wrong current password (never routes to the login gate)", async () => {
    postMock.mockRejectedValue(
      new ApiError(400, "Your current password is incorrect.", "invalid_current_password"),
    );
    const user = userEvent.setup();
    renderScreen();

    await user.type(screen.getByLabelText("Current password"), "wrong");
    await user.type(screen.getByLabelText("New password"), "new-pass-2");
    await user.type(screen.getByLabelText("Confirm new password"), "new-pass-2");
    await user.click(screen.getByRole("button", { name: /Update password/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Your current password is incorrect.",
    );
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("rejects mismatched new passwords before submitting", async () => {
    const user = userEvent.setup();
    renderScreen();

    await user.type(screen.getByLabelText("Current password"), "old-pass-1");
    await user.type(screen.getByLabelText("New password"), "new-pass-2");
    await user.type(screen.getByLabelText("Confirm new password"), "different");
    await user.click(screen.getByRole("button", { name: /Update password/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("don't match");
    expect(postMock).not.toHaveBeenCalled();
  });

  it("signs out via the single Auth.js signOut affordance and lands on the Landing Page", async () => {
    const user = userEvent.setup();
    renderScreen();

    await user.click(screen.getByRole("button", { name: /Sign out/ }));

    await waitFor(() => expect(signOutMock).toHaveBeenCalled());
    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/"));
  });

  it("opens the delete modal which confirms the account deletion and redirects with ?deleted=1", async () => {
    deleteMock.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderScreen();

    await user.click(screen.getByRole("button", { name: /Delete account/ }));
    expect(screen.getByText("Delete your account?")).toBeInTheDocument();
    const dialog = screen.getByRole("dialog");

    // The confirm button stays disabled until the email matches.
    const confirm = within(dialog).getByRole("button", { name: "Delete account" });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText(/type/i), "user@example.com");
    expect(confirm).toBeEnabled();

    await user.click(confirm);
    expect(deleteMock).toHaveBeenCalledWith("/v1/account");
    await waitFor(() => expect(signOutMock).toHaveBeenCalled());
    await waitFor(() => expect(pushMock).toHaveBeenCalledWith("/?deleted=1"));
  });

  it("surfaces a partial-failure deletion honestly — account stays signed in and intact", async () => {
    deleteMock.mockRejectedValue(
      new ApiError(500, "Your account was not deleted", "account_deletion_partial_failed"),
    );
    const user = userEvent.setup();
    renderScreen();

    await user.click(screen.getByRole("button", { name: /Delete account/ }));
    const dialog = screen.getByRole("dialog");
    await user.type(screen.getByLabelText(/type/i), "user@example.com");
    await user.click(within(dialog).getByRole("button", { name: "Delete account" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/account is intact/i);
    // Never signed out, never redirected — a false success is impossible.
    expect(signOutMock).not.toHaveBeenCalled();
    expect(pushMock).not.toHaveBeenCalled();
  });
});
