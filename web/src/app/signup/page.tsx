import type { Metadata } from "next";
import { AuthCard } from "@/components/auth/AuthCard";
import { SignupForm } from "@/components/auth/SignupForm";

export const metadata: Metadata = { title: "Sign up" };

/** Route: `/signup` — see docs/Groundwork/screens/02-sign-up-login.md */
export default function Page() {
  return (
    <AuthCard
      title="Create your account"
      toggle={{
        href: "/login",
        prompt: "Already have an account?",
        label: "Log in",
      }}
    >
      <SignupForm />
    </AuthCard>
  );
}
