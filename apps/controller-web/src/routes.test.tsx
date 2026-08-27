import { describe, expect, it } from "vitest";
import { fixturePage, nodeFixtures } from "@cable/api-client";
import type { NodeSearch } from "./routes";

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
