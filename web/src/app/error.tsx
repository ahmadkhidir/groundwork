"use client";

import { useEffect } from "react";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";

/**
 * Global error boundary.
 *
 * Every screen spec in screens/ requires an error state, and this is the fallback for the ones a
 * feature slice has not yet specialized. It states plainly that something failed and offers a
 * retry — it never renders a blank page or a spinner that will not resolve.
 */
export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    // The digest correlates this render with the server-side log entry.
    console.error("Unhandled error", error);
  }, [error]);

  return (
    <main id="main-content" className="mx-auto w-full max-w-xl p-xl">
      <Card>
        <h1 className="text-h2 text-text-primary">Something went wrong</h1>
        <p className="mt-sm text-body text-text-secondary">
          The page could not be loaded. Your data is unaffected.
        </p>
        {error.digest && (
          <p className="mt-sm font-mono text-body-small text-text-secondary">
            Reference: {error.digest}
          </p>
        )}
        <div className="mt-lg">
          <Button onClick={reset}>Try again</Button>
        </div>
      </Card>
    </main>
  );
}
