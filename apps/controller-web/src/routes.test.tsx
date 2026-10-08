import { describe, expect, it, vi } from "vitest";
import { fixturePage, nodeFixtures } from "@cable/api-client";
import type { NodeSearch } from "./kit";
import { disposeNodeCapture, requestNodeScreenshot, ScreenshotLifecycleError } from "./screens";

describe("controller route fixtures", () => {
  it("keeps node list state in bounded, typed search parameters", () => {
    const search: NodeSearch = { limit: 25, search: "lobby", sort: "name", order: "asc" };
    const page = fixturePage(nodeFixtures, search);
    expect(page.items[0]?.name).toBe("Lobby display");
    expect(page.page.hasMore).toBe(false);
  });

  it("never returns an unbounded page", () => {
    expect(fixturePage(nodeFixtures, { limit: 500 }).items).toHaveLength(nodeFixtures.length);
  });
});
describe("screenshot lifecycle", () => {
  it("claims the bounded job lifecycle and downloads content only after completion", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith("/screenshots")) return new Response(JSON.stringify({ id: "job-1", status: "queued" }), { status: 201, headers: { "Content-Type": "application/json" } });
      if (url.endsWith("/screenshots/job-1")) return new Response(JSON.stringify({ id: "job-1", status: "completed", completionAt: "2026-09-02T12:00:00Z", resultArtifactDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", resultMediaType: "image/png" }), { headers: { "Content-Type": "application/json" } });
      return new Response(new Blob(["png bytes"], { type: "image/png" }), { headers: { "Content-Type": "image/png" } });
    });
    const createUrl = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:capture");
    try {
      const result = await requestNodeScreenshot("node-1", { pollIntervalMs: 1, timeoutMs: 2_000 });
      expect(result.job.status).toBe("completed");
      expect(result.capture).toMatchObject({ url: "blob:capture", capturedAt: "2026-09-02T12:00:00Z", provenance: "Node screenshot · 0123456789ab" });
      expect(fetchMock).toHaveBeenCalledTimes(3);
      expect(String(fetchMock.mock.calls[2]?.[0])).toContain("/content");
    } finally {
      createUrl.mockRestore();
      fetchMock.mockRestore();
    }
  });

  it("surfaces typed unsupported jobs without fetching content", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ id: "job-2", status: "unsupported", errorReason: "renderer too old" }), { status: 201, headers: { "Content-Type": "application/json" } }));
    try {
      await expect(requestNodeScreenshot("legacy-node", { timeoutMs: 2_000 })).rejects.toMatchObject({ code: "unsupported" });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      fetchMock.mockRestore();
    }
  });

  it("revokes owned capture URLs during disposal", () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    try {
      disposeNodeCapture({ url: "blob:owned" });
      disposeNodeCapture();
      expect(revoke).toHaveBeenCalledTimes(1);
      expect(revoke).toHaveBeenCalledWith("blob:owned");
    } finally {
      revoke.mockRestore();
    }
  });

  it("keeps screenshot errors distinct from ordinary errors", () => {
    const error = new ScreenshotLifecycleError("expired", "expired");
    expect(error.name).toBe("ScreenshotLifecycleError");
    expect(error.code).toBe("expired");
  });
  it("supports caller cancellation without waiting for the timeout", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockRejectedValue(new DOMException("Aborted", "AbortError"));
    const abort = new AbortController();
    abort.abort();
    try {
      await expect(requestNodeScreenshot("node-3", { signal: abort.signal, timeoutMs: 2_000 })).rejects.toMatchObject({ code: "cancelled" });
      expect(fetchMock).not.toHaveBeenCalled();
    } finally {
      fetchMock.mockRestore();
    }
  });
});
