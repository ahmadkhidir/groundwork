/**
 * HTTP client for the Go API service.
 *
 * EVERY request to the backend goes through here — never a bare `fetch` in a component. That gives
 * one place for base URL resolution, credential handling, error shaping, and cancellation, and it
 * keeps the Next.js layer a thin BFF/rendering tier per architecture.md's boundary rule.
 *
 * The Next.js layer must never orchestrate runs, touch the engine, or mutate the semantic layer.
 * It renders, routes, handles the auth session, and proxies. All of that work lives behind this
 * client, in Go.
 */

/** Base URL of the Go API service. Server-side and client-side reads resolve to the same origin. */
const API_BASE_URL: string =
  process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

/**
 * The Auth.js session JWE, fetched from the web tier's BFF route (app/api/session-token) and sent
 * to the Go API as `Authorization: Bearer`. With the web on Vercel and the API on a separate
 * domain, a cross-origin cookie cannot reach the API — the bearer header is the production
 * transport; `credentials: "include"` still covers the local-development cookie path.
 *
 * Cached per page load; refreshed on a 401 (Auth.js re-encodes the cookie on session reads, so the
 * JWE can rotate). null means signed-out.
 */
let sessionTokenPromise: Promise<string | null> | null = null;

async function fetchSessionToken(): Promise<string | null> {
  try {
    const res = await fetch("/api/session-token", { cache: "no-store" });
    if (!res.ok) return null;
    const body = (await res.json()) as { token?: string | null };
    return body.token ?? null;
  } catch {
    // The BFF route is same-origin; unreachable means the web tier itself is down. Return null —
    // the API will reject the request either way.
    return null;
  }
}

function getSessionToken(refresh = false): Promise<string | null> {
  if (refresh || sessionTokenPromise === null) {
    sessionTokenPromise = fetchSessionToken();
  }
  return sessionTokenPromise;
}

/**
 * Returns the Auth.js session JWE for transports that cannot use the fetch-based `api` helper —
 * currently the XHR file upload (web/src/components/upload/uploadFile.ts), which needs per-file
 * progress events fetch does not provide. The upload attaches it as `Authorization: Bearer`, the
 * same credential the fetch path sends on every request.
 */
export function getSessionTokenForTransport(): Promise<string | null> {
  return getSessionToken();
}

/**
 * A structured error from the API.
 *
 * Carries the HTTP status so callers can branch on it — 401 to the login gate, 403 to a paywall or
 * read-only notice, 5xx to the error state — without string-matching a message.
 */
export class ApiError extends Error {
  readonly status: number;
  /** Stable machine-readable code from the API, when supplied. */
  readonly code: string;
  /** Correlation id from the response, for matching a UI report against server logs. */
  readonly requestId: string;

  constructor(
    status: number,
    message: string,
    code = "",
    requestId = "",
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }

  /** The session is missing or expired; the caller should route to the login gate. */
  get isUnauthenticated(): boolean {
    return this.status === 401;
  }

  /**
   * Authenticated but not permitted — a read-only member attempting a mutation, or a tier gate.
   * Distinct from `isUnauthenticated`: re-authenticating will not help.
   */
  get isForbidden(): boolean {
    return this.status === 403;
  }

  /** A transport-level failure: the user is offline or the API is unreachable. */
  get isOffline(): boolean {
    return this.status === 0;
  }
}

/** Shape the Go service uses for error responses. */
interface ApiErrorBody {
  error?: string;
  message?: string;
  code?: string;
}

export interface RequestOptions {
  /** Cancels the request. Wire this to component unmount and to route changes. */
  signal?: AbortSignal;
  /** Extra headers merged over the defaults. */
  headers?: Record<string, string>;
  /**
   * Next.js caching behavior. Defaults to "no-store": workspace, run, and report data is
   * user-specific and live, so caching it by default would be wrong far more often than right.
   */
  cache?: RequestCache;
}

/**
 * Performs a JSON request against the API and returns the parsed body.
 *
 * @throws {ApiError} on any non-2xx response, or on a transport failure (status 0).
 */
async function request<TResponse>(
  method: string,
  path: string,
  body?: unknown,
  options: RequestOptions = {},
): Promise<TResponse> {
  const url = `${API_BASE_URL}${path}`;
  let token = await getSessionToken();

  let response: Response;
  let attempt = 0;
  for (;;) {
    attempt += 1;

    const headers: Record<string, string> = {
      Accept: "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options.headers,
    };

    if (body !== undefined && !(body instanceof FormData)) {
      headers["Content-Type"] = "application/json";
    }

    try {
      response = await fetch(url, {
        method,
        headers,
        body:
          body === undefined
            ? undefined
            : body instanceof FormData
              ? body
              : JSON.stringify(body),
      // Session cookies must travel to the API service on every request (local dev transport;
      // production uses the bearer header above).
      credentials: "include",
        signal: options.signal,
        cache: options.cache ?? "no-store",
      });
    } catch (cause) {
      // An aborted request is intentional control flow, not an error to surface to the user.
      if (cause instanceof DOMException && cause.name === "AbortError") {
        throw cause;
      }
      // Status 0 distinguishes "never reached the server" from any HTTP response, which is what the
      // offline state on every screen keys off.
      throw new ApiError(
        0,
        "Could not reach Groundwork. Check your connection and try again.",
      );
    }

    // A single 401 retry with a refreshed token: Auth.js re-encodes the session cookie on session
    // reads, so a cached JWE can go stale while the session is still valid. Only retried once,
    // and only when a bearer was actually attached — a genuinely signed-out session (no token)
    // fails immediately and routes to the login gate.
    if (response.status === 401 && token !== null && attempt === 1) {
      const fresh = await getSessionToken(true);
      if (fresh && fresh !== token) {
        token = fresh;
        continue;
      }
    }
    break;
  }

  const requestId = response.headers.get("x-request-id") ?? "";

  if (!response.ok) {
    let message = `Request failed with status ${response.status}`;
    let code = "";

    // A non-JSON error body (a proxy's HTML 502, say) must not mask the real status.
    try {
      const parsed = (await response.json()) as ApiErrorBody;
      message = parsed.error ?? parsed.message ?? message;
      code = parsed.code ?? "";
    } catch {
      // Keep the status-derived message.
    }

    throw new ApiError(response.status, message, code, requestId);
  }

  // 204 and other empty bodies are valid successes; don't try to parse them.
  if (response.status === 204) {
    return undefined as TResponse;
  }

  const text = await response.text();
  if (text.length === 0) {
    return undefined as TResponse;
  }

  return JSON.parse(text) as TResponse;
}

/** Typed verb helpers. Prefer these over calling `request` directly. */
export const api = {
  get: <TResponse>(path: string, options?: RequestOptions) =>
    request<TResponse>("GET", path, undefined, options),

  post: <TResponse>(path: string, body?: unknown, options?: RequestOptions) =>
    request<TResponse>("POST", path, body, options),

  patch: <TResponse>(path: string, body?: unknown, options?: RequestOptions) =>
    request<TResponse>("PATCH", path, body, options),

  put: <TResponse>(path: string, body?: unknown, options?: RequestOptions) =>
    request<TResponse>("PUT", path, body, options),

  delete: <TResponse>(path: string, options?: RequestOptions) =>
    request<TResponse>("DELETE", path, undefined, options),
};

/** The API base URL, for the rare caller that needs to build a URL itself (SSE, file downloads). */
export function apiUrl(path: string): string {
  return `${API_BASE_URL}${path}`;
}

/**
 * Subscribes to a server-sent event stream from the API.
 *
 * Run progress is streamed rather than polled, so the Run Viewer can show each DAG step transition
 * as it happens — which is what makes engine and run state honestly visible rather than hidden
 * behind a spinner.
 *
 * Implemented with fetch streaming rather than `EventSource`: EventSource cannot set headers, and
 * in production the bearer Authorization header is the only credential that reaches the API from
 * the Vercel-hosted web tier. Reconnection mirrors EventSource semantics (backoff, capped) and the
 * server replays a `run.status` snapshot on each new connection, which the viewer's per-connection
 * snapshot guard already expects.
 *
 * @returns an unsubscribe function; always call it on unmount.
 */
export function subscribeToStream(
  path: string,
  handlers: {
    onMessage: (data: string) => void;
    onError?: (error: unknown) => void;
    onOpen?: () => void;
  },
): () => void {
  let closed = false;
  let controller: AbortController | null = null;
  let retryMs = 1000;

  const connect = async () => {
    if (closed) return;
    const token = await getSessionToken();
    if (closed) return;

    controller = new AbortController();
    try {
      const res = await fetch(apiUrl(path), {
        headers: {
          Accept: "text/event-stream",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        credentials: "include",
        cache: "no-store",
        signal: controller.signal,
      });
      if (closed) return;
      if (!res.ok || !res.body) {
        throw new Error(`stream failed with status ${res.status}`);
      }

      handlers.onOpen?.();
      retryMs = 1000; // a successful connection resets the backoff

      // Parse SSE frames: fields separated by \n, frames by a blank line. Only `data:` fields
      // carry a message; multi-line data joins with \n exactly as EventSource delivers it.
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let boundary: number;
        while ((boundary = buffer.indexOf("\n\n")) >= 0) {
          const frame = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          const data = frame
            .split("\n")
            .filter((line) => line.startsWith("data:"))
            .map((line) => line.slice("data:".length).trimStart())
            .join("\n");
          if (data) handlers.onMessage(data);
        }
      }
      // The server closed the stream (terminal state or the 60s request ceiling). Reconnect like
      // EventSource would; the server replays the current state on the new connection.
      throw new Error("stream closed by the server");
    } catch (error) {
      if (closed) return;
      handlers.onError?.(error);
      if (!closed) {
        controller = null;
        const delay = retryMs;
        retryMs = Math.min(retryMs * 2, 10_000);
        setTimeout(() => void connect(), delay);
      }
    }
  };

  void connect();

  return () => {
    closed = true;
    controller?.abort();
  };
}
