import { describe, expect, it } from "vitest";
import { fixturePage, nodeFixtures } from "./index";

describe("fixture pagination contract", () => {
  it("uses opaque cursors and bounded limits", () => {
    const first = fixturePage(nodeFixtures, { limit: 1 });
    expect(first.items).toHaveLength(1);
    expect(first.page.nextCursor).toBe("1");
    expect(fixturePage(nodeFixtures, { cursor: first.page.nextCursor ?? undefined, limit: 100 }).items[0]?.id).toBe("node-002");
  });

  it("searches server-side without mutating source items", () => {
    const result = fixturePage(nodeFixtures, { search: "studio" });
    expect(result.items.map((node) => node.id)).toEqual(["node-002"]);
    expect(nodeFixtures).toHaveLength(3);
  });
});
