import { expect, test } from "vitest";
import { mapRemoteEvent, maxInputEvents, parseInputBody, parseRemoteBody, webURL } from "./remote-input";

const size = { width: 1921, height: 1081 };

test("pointer coordinates scale to view pixels and clamp to its edges", () => {
  const cursor = { x: 0, y: 0 };
  expect(mapRemoteEvent({ type: "pointer", x: 0.5, y: 1, state: "move" }, size, cursor)).toEqual([
    { kind: "mouse", type: "mouseMove", x: 960, y: 1080, button: "left", clickCount: 0 },
  ]);
  expect(mapRemoteEvent({ type: "pointer", x: -3, y: 7 }, size, cursor)[0]).toMatchObject({ x: 0, y: 1080 });
  expect(cursor).toEqual({ x: 0, y: 1080 });
});

test("click presses and releases the requested button at the target", () => {
  const actions = mapRemoteEvent({ type: "pointer", x: 1, y: 0, state: "click", button: "right" }, size, { x: 0, y: 0 });
  expect(actions.map((action) => action.kind === "mouse" && [action.type, action.x, action.y, action.button])).toEqual([
    ["mouseMove", 1920, 0, "right"],
    ["mouseDown", 1920, 0, "right"],
    ["mouseUp", 1920, 0, "right"],
  ]);
});

test("positive wheel deltaY scrolls content down using Chromium's inverted sign at the cursor", () => {
  const cursor = { x: 0, y: 0 };
  mapRemoteEvent({ type: "pointer", x: 0.5, y: 0.5 }, size, cursor);
  expect(mapRemoteEvent({ type: "wheel", deltaY: 1.5 }, size, cursor)).toEqual([{ kind: "wheel", x: 960, y: 540, deltaX: -0, deltaY: -150 }]);
  expect(mapRemoteEvent({ type: "wheel", deltaX: -40, deltaY: 0 }, size, cursor)).toEqual([{ kind: "wheel", x: 960, y: 540, deltaX: 1000, deltaY: -0 }]);
  expect(mapRemoteEvent({ type: "wheel", deltaX: 0, deltaY: 0 }, size, cursor)).toEqual([]);
});

test("text becomes char events with newlines sent as Return", () => {
  expect(mapRemoteEvent({ type: "text", text: "a \n" }, size, { x: 0, y: 0 })).toEqual([
    { kind: "key", type: "char", keyCode: "a" },
    { kind: "key", type: "char", keyCode: " " },
    { kind: "key", type: "keyDown", keyCode: "Return" },
    { kind: "key", type: "char", keyCode: "\r" },
    { kind: "key", type: "keyUp", keyCode: "Return" },
  ]);
});

test("named keys map to Electron key codes and TV-only keys are ignored", () => {
  const cursor = { x: 0, y: 0 };
  expect(mapRemoteEvent({ type: "key", key: "ArrowUp" }, size, cursor)).toEqual([
    { kind: "key", type: "keyDown", keyCode: "Up" },
    { kind: "key", type: "keyUp", keyCode: "Up" },
  ]);
  expect(mapRemoteEvent({ type: "key", key: "PageDown" }, size, cursor).map((action) => action.kind === "key" && action.keyCode)).toEqual(["PageDown", "PageDown"]);
  expect(mapRemoteEvent({ type: "key", key: "BrowserBack" }, size, cursor)).toEqual([{ kind: "navigate", action: "back" }]);
  for (const key of ["VolumeUp", "Mute", "ChannelUp", "Guide", "Info", "ChannelNumber:4"]) {
    expect(mapRemoteEvent({ type: "key", key }, size, cursor)).toEqual([]);
  }
});

test("input bodies are bounded and typed", () => {
  expect(parseInputBody({ events: Array.from({ length: maxInputEvents + 1 }, () => ({ type: "key", key: "Tab" })) })).toBeTypeOf("string");
  expect(parseInputBody({ events: [{ type: "pointer", x: Number.NaN }] })).toBeTypeOf("string");
  expect(parseInputBody({ events: [{ type: "launch" }] })).toBeTypeOf("string");
  expect(parseInputBody({ events: [{ type: "pointer", x: 0.1, y: 0.2, state: "down" }] })).toEqual([{ type: "pointer", x: 0.1, y: 0.2, state: "down" }]);
});

test("web targets must be absolute HTTP(S) without credentials", () => {
  expect(webURL("https://example.com/path")?.origin).toBe("https://example.com");
  expect(webURL("http://user:pw@example.com")).toBeUndefined();
  expect(webURL("file:///etc/passwd")).toBeUndefined();
  expect(webURL("/relative")).toBeUndefined();
});

test("remote descriptors require url and showQr, and only QR targets must be HTTP(S)", () => {
  expect(parseRemoteBody({ url: "https://control.example/remote/a#token=x", showQr: true })).toEqual({ qrURL: "https://control.example/remote/a#token=x" });
  expect(parseRemoteBody({ url: "anything", showQr: false })).toEqual({});
  expect(parseRemoteBody({ url: "javascript:alert(1)", showQr: true })).toBe("renderer remote URL must be HTTP(S)");
  expect(parseRemoteBody({ url: "https://control.example/" })).toBe("renderer remote must be {url, showQr}");
  expect(parseRemoteBody({ url: `https://control.example/${"a".repeat(2048)}`, showQr: true })).toBe("renderer remote must be {url, showQr}");
  expect(parseRemoteBody(null)).toBe("renderer remote must be {url, showQr}");
});
