import type { Metadata } from "next";
import { AuthCard } from "@/components/auth/AuthCard";
import { LoginForm } from "@/components/auth/LoginForm";
import { QueryParam } from "@/lib/routes";

export const metadata: Metadata = { title: "Log in" };

/** Route: `/login` — see docs/Groundwork/screens/02-sign-up-login.md */
export default async function Page(props: PageProps<"/login">) {
  // searchParams is a Promise in Next 16; synchronous access was removed.
  const searchParams = await props.searchParams;
  const nextParam = searchParams[QueryParam.Next];
  const redirectTo = typeof nextParam === "string" ? nextParam : undefined;

  return (
    <AuthCard
      title="Log in"
      toggle={{
        href: "/signup",
        prompt: "New to Groundwork?",
        label: "Sign up",
      }}
    >
      <LoginForm redirectTo={redirectTo} />
    </AuthCard>
  );
}
