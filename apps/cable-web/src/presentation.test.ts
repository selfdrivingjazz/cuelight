import { describe, expect, it } from "vitest";
import { loadPresentation, presentationFixture } from "./presentation";

describe("presentation", () => {
  it("accepts typed fixture data", async () => {
    const value = await loadPresentation({ read: async () => presentationFixture });
    expect(value.revision.digest).toContain("sha256:");
    expect(value.occurrences[0]?.media.kind).toBe("video");
  });

  it("rejects malformed payloads", async () => {
    await expect(loadPresentation({ read: async () => ({ revision: {} }) })).rejects.toThrow("invalid presentation");
  });
});
