"use client";

import { useEffect, useState } from "react";

/**
 * Tracks the browser's `navigator.onLine` signal, live.
 *
 * This is optimistic — `navigator.onLine` can read `true` on a captive portal with no real route
 * out — but it is the only connectivity signal available without polling the API, and it is what
 * every screen's Offline state (per screens/02-sign-up-login.md and others) assumes.
 *
 * Not auth-specific in principle; it lives here because auth is this slice's only consumer so far.
 * Worth promoting to a shared `src/lib` hook once a second screen needs it.
 */
export function useOnlineStatus(): boolean {
  // Lazy initializer rather than a hardcoded `true`: `navigator` doesn't exist during the server
  // render, so the guard falls back to "online" there and reads the real value on the client's
  // first render — before mount, not inside an effect, which avoids the extra render an
  // effect-driven setState would cause (react-hooks/set-state-in-effect).
  const [online, setOnline] = useState(() =>
    typeof navigator === "undefined" ? true : navigator.onLine,
  );

  useEffect(() => {
    const handleOnline = () => setOnline(true);
    const handleOffline = () => setOnline(false);

    window.addEventListener("online", handleOnline);
    window.addEventListener("offline", handleOffline);
    return () => {
      window.removeEventListener("online", handleOnline);
      window.removeEventListener("offline", handleOffline);
    };
  }, []);

  return online;
}
