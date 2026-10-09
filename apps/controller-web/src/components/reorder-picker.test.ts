import type { components } from "@cuelight/api-client";
import { describe, expect, it } from "vitest";
import { pickableMedia, toggleMediaSelection } from "./MediaPicker";
import { dropIndexFor, moveItem } from "./SortableList";

type Media = components["schemas"]["Media"];
type MediaPage = components["schemas"]["MediaPage"];
const media = (assetId: string) => ({ id: `media-${assetId}`, kind: "file", assetId, title: assetId }) as Media;
const webMedia = (id: string) => ({ id, kind: "web", url: `https://${id}.example`, title: id }) as Media;
const page = (ids: string[]): MediaPage => ({ items: ids.map(media), page: { nextCursor: null, previousCursor: null, hasMore: false } });

describe("moveItem", () => {
  it("moves forwards and backwards without mutating the input", () => {
    const items = ["a", "b", "c", "d"];
    expect(moveItem(items, 0, 2)).toEqual(["b", "c", "a", "d"]);
    expect(moveItem(items, 3, 0)).toEqual(["d", "a", "b", "c"]);
    expect(items).toEqual(["a", "b", "c", "d"]);
  });

  it("ignores out-of-range moves", () => {
    expect(moveItem(["a", "b"], 0, 5)).toEqual(["a", "b"]);
    expect(moveItem(["a", "b"], -1, 0)).toEqual(["a", "b"]);
  });
});

describe("dropIndexFor", () => {
  const mids = [10, 30, 50, 70];
  it("maps pointer position to the destination index of the dragged row", () => {
    expect(dropIndexFor(mids, 0, 2)).toBe(0);
    expect(dropIndexFor(mids, 45, 0)).toBe(1);
    expect(dropIndexFor(mids, 55, 0)).toBe(2);
    expect(dropIndexFor(mids, 999, 1)).toBe(3);
  });
  it("keeps the row in place while the pointer stays within its neighbours' midpoints", () => {
    expect(dropIndexFor(mids, 25, 1)).toBe(1);
    expect(dropIndexFor(mids, 45, 1)).toBe(1);
  });
});

describe("toggleMediaSelection", () => {
  it("multi-select appends in pick order and removes on second toggle", () => {
    const picked = toggleMediaSelection(toggleMediaSelection([], media("x"), true), media("y"), true);
    expect(picked.map((item) => item.assetId)).toEqual(["x", "y"]);
    expect(toggleMediaSelection(picked, media("x"), true).map((item) => item.assetId)).toEqual(["y"]);
  });
  it("single-select replaces the choice", () => {
    expect(toggleMediaSelection([media("x")], media("y"), false).map((item) => item.assetId)).toEqual(["y"]);
    expect(toggleMediaSelection([media("x")], media("x"), false)).toEqual([]);
  });
});

describe("pickableMedia", () => {
  it("flattens pages, drops duplicates across cursors, and hides excluded assets", () => {
    const result = pickableMedia([page(["a", "b"]), page(["b", "c", "d"])], new Set(["c"]));
    expect(result.map((item) => item.assetId)).toEqual(["a", "b", "d"]);
  });
  it("keeps web pages distinct even though they have no asset id", () => {
    const result = pickableMedia([{ items: [webMedia("w1"), webMedia("w2"), media("a")], page: { nextCursor: null, previousCursor: null, hasMore: false } }], new Set(["a"]));
    expect(result.map((item) => item.id)).toEqual(["w1", "w2"]);
    expect(toggleMediaSelection([webMedia("w1")], webMedia("w2"), true).map((item) => item.id)).toEqual(["w1", "w2"]);
  });
});
