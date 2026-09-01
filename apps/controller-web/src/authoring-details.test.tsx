import { describe, expect, it } from "vitest";
import { authoringLinks, formatDuration, formatTimestamp } from "./authoring-details";

describe("Cable authoring detail workflows", () => {
  it("builds stable cross-links with encoded resource IDs", () => {
    expect(authoringLinks.playlist("playlist/one")).toEqual({ href: "/cable/playlists/playlist%2Fone", label: "View playlist" });
    expect(authoringLinks.asset("asset one").href).toBe("/assets/asset%20one");
    expect(authoringLinks.release("r-1").href).toBe("/cable/releases/r-1");
  });

  it("formats source timing without hiding invalid values", () => {
    expect(formatDuration(125)).toBe("2m 5s");
    expect(formatDuration(-1)).toBe("Unknown duration");
    expect(formatTimestamp("not-a-time")).toBe("Unknown time");
  });
});
