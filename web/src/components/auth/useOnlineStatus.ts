"use client";

import { useSyncExternalStore } from "react";

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

/** Subscribes to connectivity changes. Module-scope so its identity is stable across renders. */
function subscribe(onStoreChange: () => void): () => void {
  window.addEventListener("online", onStoreChange);
  window.addEventListener("offline", onStoreChange);
  return () => {
    window.removeEventListener("online", onStoreChange);
    window.removeEventListener("offline", onStoreChange);
  };
}

/** Client snapshot: the live browser value. */
function getSnapshot(): boolean {
  return navigator.onLine;
}

/**
 * Server snapshot: always "online".
 *
 * This MUST be a separate function rather than reusing `getSnapshot` with a `typeof navigator`
 * guard. Node 24 defines a global `navigator` object, but WITHOUT an `onLine` property — so a
 * `typeof navigator === "undefined"` check passes on the server, `navigator.onLine` evaluates to
 * `undefined`, and the whole form server-renders in its OFFLINE state. That produced a real
 * hydration mismatch: the server sent the offline banner, the client replaced it with the form.
 *
 * Rendering "online" on the server is also the right default on its own merits — the server cannot
 * know a client's connectivity, and a page that arrives claiming the user is offline is wrong far
 * more often than it is right.
 */
function getServerSnapshot(): boolean {
  return true;
}

/**
 * Returns whether the browser currently believes it has a network connection.
 *
 * Uses `useSyncExternalStore` rather than `useState` + `useEffect`: it is React's designated API for
 * subscribing to external mutable state, it takes an explicit server snapshot so hydration cannot
 * mismatch, and it avoids the extra post-mount render an effect-driven `setState` would cost.
 */
export function useOnlineStatus(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
