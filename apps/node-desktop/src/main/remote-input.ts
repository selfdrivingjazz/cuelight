// Pure translation from Cable remote events to Electron webContents input events.
// Kept free of Electron runtime imports so it can be unit-tested.

export interface RemoteEvent {
  sequence?: number;
  type: "key" | "text" | "pointer" | "wheel";
  key?: string;
  text?: string;
  x?: number;
  y?: number;
  deltaX?: number;
  deltaY?: number;
  button?: "left" | "right" | "middle";
  state?: "move" | "down" | "up" | "click";
}

export type MouseButton = "left" | "right" | "middle";

export type InputAction =
  | { kind: "mouse"; type: "mouseMove" | "mouseDown" | "mouseUp"; x: number; y: number; button: MouseButton; clickCount: number }
  | { kind: "wheel"; x: number; y: number; deltaX: number; deltaY: number }
  | { kind: "key"; type: "keyDown" | "keyUp" | "char"; keyCode: string }
  | { kind: "navigate"; action: "back" | "forward" | "reload" };

export interface ViewSize { width: number; height: number }
export interface Point { x: number; y: number }

export const maxInputEvents = 256;
const wheelUnit = 100;
const maxText = 1024;

const keyCodes: Record<string, string> = {
  ArrowUp: "Up",
  ArrowDown: "Down",
  ArrowLeft: "Left",
  ArrowRight: "Right",
  Backspace: "Backspace",
  Delete: "Delete",
  Enter: "Return",
  Escape: "Escape",
  Tab: "Tab",
  Home: "Home",
  End: "End",
  PageUp: "PageUp",
  PageDown: "PageDown",
  Space: "Space",
};

const navigation: Record<string, "back" | "forward" | "reload"> = {
  BrowserBack: "back",
  BrowserForward: "forward",
  BrowserReload: "reload",
};

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function finite(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

/** Parses an untrusted /input body into remote events; returns an error string on invalid input. */
export function parseInputBody(body: Record<string, unknown>): RemoteEvent[] | string {
  const events = body.events;
  if (!Array.isArray(events)) return "events must be an array";
  if (events.length > maxInputEvents) return `at most ${maxInputEvents} events are accepted`;
  const parsed: RemoteEvent[] = [];
  for (const value of events) {
    if (!value || typeof value !== "object" || Array.isArray(value)) return "event must be an object";
    const event = value as Record<string, unknown>;
    if (event.type !== "key" && event.type !== "text" && event.type !== "pointer" && event.type !== "wheel") return "event type is invalid";
    if (event.text !== undefined && (typeof event.text !== "string" || event.text.length > maxText)) return "event text is invalid";
    if (event.key !== undefined && typeof event.key !== "string") return "event key is invalid";
    for (const field of ["x", "y", "deltaX", "deltaY"] as const) {
      if (event[field] !== undefined && !finite(event[field])) return `event ${field} is invalid`;
    }
    if (event.button !== undefined && event.button !== "left" && event.button !== "right" && event.button !== "middle") return "event button is invalid";
    if (event.state !== undefined && event.state !== "move" && event.state !== "down" && event.state !== "up" && event.state !== "click") return "event state is invalid";
    parsed.push(event as unknown as RemoteEvent);
  }
  return parsed;
}

/**
 * Maps one remote event to Electron input actions. `cursor` is the last pointer
 * position in view pixels and is updated by pointer events; wheel events scroll there.
 */
export function mapRemoteEvent(event: RemoteEvent, size: ViewSize, cursor: Point): InputAction[] {
  switch (event.type) {
    case "pointer": {
      if (!finite(event.x) || !finite(event.y)) return [];
      const x = Math.round(clamp(event.x, 0, 1) * Math.max(0, size.width - 1));
      const y = Math.round(clamp(event.y, 0, 1) * Math.max(0, size.height - 1));
      cursor.x = x;
      cursor.y = y;
      const button = event.button ?? "left";
      const mouse = (type: "mouseMove" | "mouseDown" | "mouseUp"): InputAction => ({ kind: "mouse", type, x, y, button, clickCount: type === "mouseMove" ? 0 : 1 });
      switch (event.state ?? "move") {
        case "move": return [mouse("mouseMove")];
        case "down": return [mouse("mouseMove"), mouse("mouseDown")];
        case "up": return [mouse("mouseMove"), mouse("mouseUp")];
        case "click": return [mouse("mouseMove"), mouse("mouseDown"), mouse("mouseUp")];
      }
      return [];
    }
    case "wheel": {
      const dx = finite(event.deltaX) ? clamp(event.deltaX, -10, 10) : 0;
      const dy = finite(event.deltaY) ? clamp(event.deltaY, -10, 10) : 0;
      if (dx === 0 && dy === 0) return [];
      // Chromium wheel deltas are positive when scrolling content up/left, so a
      // DOM-style positive deltaY (scroll down) is negated.
      return [{ kind: "wheel", x: cursor.x, y: cursor.y, deltaX: -dx * wheelUnit, deltaY: -dy * wheelUnit }];
    }
    case "text": {
      const actions: InputAction[] = [];
      for (const char of event.text ?? "") {
        if (char === "\n" || char === "\r") {
          actions.push({ kind: "key", type: "keyDown", keyCode: "Return" }, { kind: "key", type: "char", keyCode: "\r" }, { kind: "key", type: "keyUp", keyCode: "Return" });
        } else {
          actions.push({ kind: "key", type: "char", keyCode: char });
        }
      }
      return actions;
    }
    case "key": {
      const key = event.key ?? "";
      const nav = navigation[key];
      if (nav) return [{ kind: "navigate", action: nav }];
      const keyCode = keyCodes[key];
      if (!keyCode) return [];
      const actions: InputAction[] = [{ kind: "key", type: "keyDown", keyCode }];
      if (keyCode === "Space") actions.push({ kind: "key", type: "char", keyCode: " " });
      if (keyCode === "Return") actions.push({ kind: "key", type: "char", keyCode: "\r" });
      actions.push({ kind: "key", type: "keyUp", keyCode });
      return actions;
    }
  }
  return [];
}

/** Validates a URL for a `web` window: absolute http(s) without userinfo. */
export function webURL(value: string): URL | undefined {
  try {
    const parsed = new URL(value);
    if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.username || parsed.password) return undefined;
    return parsed;
  } catch {
    return undefined;
  }
}

/**
 * Validates a `{url, showQr}` remote descriptor. Returns an error message, or
 * the QR target URL (undefined when no QR should be shown).
 */
export function parseRemoteBody(value: unknown): { qrURL?: string } | string {
  const remote = value as Record<string, unknown> | null;
  if (!remote || typeof remote !== "object" || typeof remote.url !== "string" || typeof remote.showQr !== "boolean" || remote.url.length > 2048) {
    return "renderer remote must be {url, showQr}";
  }
  if (!remote.showQr) return {};
  if (!webURL(remote.url)) return "renderer remote URL must be HTTP(S)";
  return { qrURL: remote.url };
}
