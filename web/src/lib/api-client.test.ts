import { beforeEach, describe, expect, it, vi } from "vitest";

type ApiClientModule = typeof import("./api-client");

/**
 * Bearer-transport tests for the API client (Vercel web + Fly API: no shared parent domain, so the
 * browser cannot send the session cookie cross-origin — the web tier's BFF route issues the JWE and
 * it travels as Authorization: Bearer).
 *
 * The module caches the fetched session token for the page lifetime, so every test imports a fresh
 * module instance (vi.resetModules) — otherwise the first test's cached token leaks into the rest.
 */
async function freshClient(): Promise<ApiClientModule> {
  vi.resetModules();
  return import("./api-client");
}

/** Controllable SSE stream body for subscribeToStream tests. */
class MockStreamSource {
  controller: ReadableStreamDefaultController<Uint8Array> | null = null;

  response(): Response {
    return new Response(
      new ReadableStream<Uint8Array>({
        start: (controller) => {
          this.controller = controller;
        },
      }),
      { status: 200, headers: { "Content-Type": "text/event-stream" } },
    );
  }

  push(data: string): void {
    this.controller?.enqueue(new TextEncoder().encode(data));
  }
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("api bearer transport", () => {
  it("fetches the session token from the BFF route and sends it as Authorization: Bearer", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ token: "jwe-1" })) // /api/session-token
      .mockResolvedValueOnce(jsonResponse({ ok: true })); // the API call
    const { api } = await freshClient();

    const out = await api.get<{ ok: boolean }>("/v1/workspaces");
    expect(out).toEqual({ ok: true });
    expect(String(fetchMock.mock.calls[0][0])).toContain("/api/session-token");
    expect(fetchMock.mock.calls[1][1]!.headers.Authorization).toBe("Bearer jwe-1");
  });

  it("refreshes the token once on a 401 and retries with the fresh token", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ token: "jwe-1" })) // session-token
      .mockResolvedValueOnce(jsonResponse({ error: "expired" }, 401)) // first API attempt
      .mockResolvedValueOnce(jsonResponse({ token: "jwe-2" })) // refreshed session-token
      .mockResolvedValueOnce(jsonResponse({ ok: true })); // retried API attempt
    const { api } = await freshClient();

    const out = await api.get<{ ok: boolean }>("/v1/workspaces");
    expect(out).toEqual({ ok: true });
    expect(fetchMock.mock.calls[1][1]!.headers.Authorization).toBe("Bearer jwe-1");
    expect(fetchMock.mock.calls[3][1]!.headers.Authorization).toBe("Bearer jwe-2");
  });

  it("does not retry a 401 when no bearer token is attached (signed out)", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ token: null })) // session-token → signed out
      .mockResolvedValueOnce(jsonResponse({ error: "nope" }, 401));
    const { api } = await freshClient();

    await expect(api.get("/v1/workspaces")).rejects.toMatchObject({
      status: 401,
      isUnauthenticated: true,
    });
    // session-token fetch + exactly one API attempt — no phantom retry.
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("keeps credentials: include for the local-development cookie transport", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ token: null }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }));
    const { api } = await freshClient();

    await api.get("/v1/workspaces");
    expect(fetchMock.mock.calls[1][1]!.credentials).toBe("include");
  });

  it("subscribes to an SSE stream via fetch with the bearer header (EventSource cannot set headers)", async () => {
    const source = new MockStreamSource();
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ token: "jwe-1" })) // session-token
      .mockImplementationOnce(() => Promise.resolve(source.response())); // the stream
    const { subscribeToStream } = await freshClient();

    const onMessage = vi.fn();
    const unsubscribe = subscribeToStream("/v1/workspaces/ws-1/runs/r1/stream", { onMessage });

    // The stream request carried the bearer header.
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][1]!.headers.Authorization).toBe("Bearer jwe-1");

    // An SSE frame is parsed and delivered to the handler.
    source.push('data: {"type":"run.status","status":"complete"}\n\n');
    await vi.waitFor(() => expect(onMessage).toHaveBeenCalledTimes(1));
    expect(onMessage).toHaveBeenCalledWith('{"type":"run.status","status":"complete"}');

    unsubscribe();
  });
});
