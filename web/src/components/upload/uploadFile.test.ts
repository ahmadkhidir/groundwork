import { beforeEach, describe, expect, it, vi } from "vitest";

type UploadFileModule = typeof import("./uploadFile");

/**
 * uploadFile is the one transport that bypasses api-client's fetch helper (XHR, for per-file
 * progress), so it must attach the production bearer header itself. These tests stub XMLHttpRequest
 * and assert the header reaches the wire.
 */
async function freshUploadFile(): Promise<UploadFileModule> {
  vi.resetModules();
  return import("./uploadFile");
}

class MockXHR {
  static instances: MockXHR[] = [];
  open = vi.fn();
  send = vi.fn();
  setRequestHeader = vi.fn();
  abort = vi.fn();
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = {
    onprogress: null,
  };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  status = 200;
  responseText = "";

  constructor() {
    MockXHR.instances.push(this);
  }
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("XMLHttpRequest", MockXHR);
  MockXHR.instances.length = 0;
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("uploadFile bearer transport", () => {
  it("attaches the BFF-issued token as Authorization: Bearer on the XHR", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ token: "jwe-1" })); // /api/session-token
    const { uploadFile } = await freshUploadFile();

    const promise = uploadFile("ws-1", new File(["data"], "orders.csv"), vi.fn());
    await vi.waitFor(() => expect(MockXHR.instances.length).toBe(1));

    const xhr = MockXHR.instances[0];
    expect(xhr.open).toHaveBeenCalledWith(
      "POST",
      expect.stringContaining("/v1/workspaces/ws-1/files"),
    );
    expect(xhr.setRequestHeader).toHaveBeenCalledWith("Authorization", "Bearer jwe-1");

    xhr.responseText = JSON.stringify({ results: [{ filename: "orders.csv" }] });
    xhr.onload?.();
    await expect(promise).resolves.toEqual({ filename: "orders.csv" });
  });

  it("does not attach an Authorization header when no session token exists (the API 401s honestly)", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ token: null })); // signed out
    const { uploadFile } = await freshUploadFile();

    const promise = uploadFile("ws-1", new File(["data"], "orders.csv"), vi.fn());
    await vi.waitFor(() => expect(MockXHR.instances.length).toBe(1));

    const xhr = MockXHR.instances[0];
    expect(xhr.setRequestHeader).not.toHaveBeenCalled();

    xhr.status = 401;
    xhr.responseText = JSON.stringify({ error: "You must be signed in to do that." });
    xhr.onload?.();
    await expect(promise).rejects.toThrow("Upload failed with status 401");
  });
});
