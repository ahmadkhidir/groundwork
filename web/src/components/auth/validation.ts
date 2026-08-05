/**
 * Shared client-side validation for the auth forms.
 *
 * Matches exactly the two conditions screens/02-sign-up-login.md's Validation Error state names —
 * "Invalid email format or empty required field" — and nothing more. No password complexity policy
 * is specified anywhere in the docs, so none is enforced here; the Go service (internal/auth) makes
 * the identical choice for the same reason.
 */
export const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function isValidEmail(email: string): boolean {
  return EMAIL_PATTERN.test(email);
}
