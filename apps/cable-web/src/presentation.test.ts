import { describe, expect, it } from "vitest";
import { HttpPresentationSource, loadPresentation, presentationFixture } from "./presentation";

describe("presentation bridge", () => {
  it("accepts typed fixture data", async () => {
    const value = await loadPresentation({ read: async () => presentationFixture });
    expect(value.revision.digest).toContain("sha256:");
    expect(value.occurrences[0]?.media.kind).toBe("video");
  });

  it("fetches the same-origin bridge without credentials or cache and signals readiness", async () => {
    const requests: Array<{ input: RequestInfo | URL; init?: RequestInit }> = [];
    const source = new HttpPresentationSource(async (input, init) => {
      requests.push({ input, init });
      const isReady = String(input) === "/bridge/v1/ready";
      return new Response(isReady ? null : JSON.stringify(presentationFixture), {
        status: isReady ? 204 : 200,
        headers: { "content-type": "application/json" },
      });
    });
    await expect(loadPresentation(source)).resolves.toEqual(presentationFixture);
    await expect(source.markReady?.()).resolves.toBeUndefined();
    expect(requests.map(({ input }) => String(input))).toEqual(["/bridge/v1/presentation", "/bridge/v1/ready"]);
    expect(requests[0]?.init).toMatchObject({ credentials: "omit", cache: "no-store" });
    expect(requests[1]?.init).toMatchObject({ method: "POST", credentials: "omit", cache: "no-store" });
  });

  it("rejects malformed bridge payloads", async () => {
    await expect(loadPresentation({ read: async () => ({ revision: {} }) })).rejects.toThrow("invalid presentation");
  });
});
