import * as React from "react";
import { useParams } from "@tanstack/react-router";

export type RemoteButton = "left" | "right" | "middle";
export type RemoteEventInput =
  | { type: "key"; key: string }
  | { type: "text"; text: string }
  | { type: "pointer"; x: number; y: number; state: "move" | "down" | "up" | "click"; button?: RemoteButton }
  | { type: "wheel"; deltaX: number; deltaY: number };

const minPostIntervalMs = 50; // ≤ 20 events/s
const isMove = (event: RemoteEventInput) => event.type === "pointer" && event.state === "move";

/**
 * Serial remote event sender: at most one POST in flight, consecutive pointer moves coalesce to
 * the latest, other events keep their order. Sequences are assigned at send time so they stay
 * strictly increasing.
 */
export function useRemoteSender(sessionId: string, token: string, onStatus: (status: "ready" | "error") => void) {
  const sequence = React.useRef(Date.now() * 1_000);
  const pending = React.useRef<RemoteEventInput[]>([]);
  const busy = React.useRef(false);
  const lastPost = React.useRef(0);
  const statusRef = React.useRef(onStatus);
  statusRef.current = onStatus;

  const pump = React.useCallback(async () => {
    if (busy.current) return;
    busy.current = true;
    try {
      while (pending.current.length > 0) {
        const wait = lastPost.current + minPostIntervalMs - Date.now();
        if (wait > 0) await new Promise((resolve) => window.setTimeout(resolve, wait));
        const event = pending.current.shift()!;
        sequence.current = Math.max(sequence.current + 1, Date.now() * 1_000);
        lastPost.current = Date.now();
        try {
          const response = await fetch(`/api/v1/remote/sessions/${encodeURIComponent(sessionId)}/events`, {
            method: "POST",
            headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
            body: JSON.stringify({ sequence: sequence.current, ...event }),
            credentials: "omit",
            cache: "no-store",
          });
          statusRef.current(response.ok ? "ready" : "error");
        } catch {
          statusRef.current("error");
        }
      }
    } finally {
      busy.current = false;
    }
  }, [sessionId, token]);

  return React.useCallback((event: RemoteEventInput) => {
    if (!token || !sessionId) return;
    const queue = pending.current;
    if (isMove(event) && queue.length > 0 && isMove(queue[queue.length - 1]!)) queue[queue.length - 1] = event;
    else queue.push(event);
    void pump();
  }, [pump, sessionId, token]);
}

const tapMaxMs = 450;
const tapMaxPx = 12;
const holdMs = 220;
const sensitivity = 1.25;
const wheelPxPerUnit = 100;
const clamp01 = (value: number) => Math.min(1, Math.max(0, value));
const round = (value: number) => Math.round(value * 10_000) / 10_000;

interface Touch { x: number; y: number; startX: number; startY: number }

export function Trackpad({ send }: { send: (event: RemoteEventInput) => void }) {
  const padRef = React.useRef<HTMLDivElement>(null);
  const cursor = React.useRef({ x: 0.5, y: 0.5 });
  const touches = React.useRef(new Map<number, Touch>());
  const gesture = React.useRef({ start: 0, maxFingers: 0, moved: false, dragging: false, scrolling: false, holdTimer: 0 as number | undefined });
  const wheelCarry = React.useRef({ x: 0, y: 0 });
  const [marker, setMarker] = React.useState({ x: 0.5, y: 0.5 });

  const pointer = (state: "move" | "down" | "up" | "click", button?: RemoteButton) =>
    send({ type: "pointer", x: round(cursor.current.x), y: round(cursor.current.y), state, ...(button ? { button } : {}) });

  const wheel = (dx: number, dy: number) => {
    const carry = wheelCarry.current;
    carry.x += dx; carry.y += dy;
    const deltaX = Math.max(-10, Math.min(10, carry.x / wheelPxPerUnit));
    const deltaY = Math.max(-10, Math.min(10, carry.y / wheelPxPerUnit));
    if (Math.abs(deltaX) < 0.2 && Math.abs(deltaY) < 0.2) return;
    carry.x = 0; carry.y = 0;
    send({ type: "wheel", deltaX: round(deltaX), deltaY: round(deltaY) });
  };

  const clearHold = () => { if (gesture.current.holdTimer) window.clearTimeout(gesture.current.holdTimer); gesture.current.holdTimer = undefined; };
  React.useEffect(() => clearHold, []);

  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const g = gesture.current;
    if (touches.current.size === 0) {
      Object.assign(g, { start: Date.now(), maxFingers: 0, moved: false, dragging: false, scrolling: false });
      wheelCarry.current = { x: 0, y: 0 };
      clearHold();
      g.holdTimer = window.setTimeout(() => {
        g.holdTimer = undefined;
        if (!g.moved && touches.current.size === 1) { g.dragging = true; pointer("down"); }
      }, holdMs);
    } else clearHold();
    touches.current.set(event.pointerId, { x: event.clientX, y: event.clientY, startX: event.clientX, startY: event.clientY });
    g.maxFingers = Math.max(g.maxFingers, touches.current.size);
  };

  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const touch = touches.current.get(event.pointerId);
    const pad = padRef.current;
    if (!touch || !pad) return;
    const dx = event.clientX - touch.x;
    const dy = event.clientY - touch.y;
    touch.x = event.clientX; touch.y = event.clientY;
    const g = gesture.current;
    if (Math.hypot(touch.x - touch.startX, touch.y - touch.startY) >= tapMaxPx) { g.moved = true; if (!g.dragging) clearHold(); }
    if (touches.current.size >= 2) {
      g.scrolling = true;
      // Average the fingers: each contributes half of the shared movement.
      wheel(-dx / touches.current.size, -dy / touches.current.size);
      return;
    }
    if (g.scrolling) return;
    const rect = pad.getBoundingClientRect();
    const speed = Math.hypot(dx, dy);
    const accel = 1 + Math.min(1.5, speed / 40);
    cursor.current = {
      x: clamp01(cursor.current.x + (dx / Math.max(rect.width, 1)) * sensitivity * accel),
      y: clamp01(cursor.current.y + (dy / Math.max(rect.height, 1)) * sensitivity * accel),
    };
    setMarker(cursor.current);
    pointer("move");
  };

  const onPointerEnd = (event: React.PointerEvent<HTMLDivElement>) => {
    if (!touches.current.delete(event.pointerId)) return;
    if (touches.current.size > 0) return;
    const g = gesture.current;
    clearHold();
    if (g.dragging) { pointer("up"); g.dragging = false; return; }
    const tap = !g.moved && Date.now() - g.start < tapMaxMs && event.type === "pointerup";
    if (!tap) return;
    pointer("click", g.maxFingers >= 2 ? "right" : undefined);
  };

  const strip = React.useRef<number | undefined>(undefined);
  return <div className="remote-trackpad-wrap">
    <div
      ref={padRef}
      className="remote-trackpad"
      role="application"
      aria-label="Trackpad"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerEnd}
      onPointerCancel={onPointerEnd}
    >
      <span className="remote-trackpad-cursor" style={{ left: `${marker.x * 100}%`, top: `${marker.y * 100}%` }} aria-hidden="true" />
    </div>
    <div
      className="remote-scrollstrip"
      role="scrollbar"
      aria-label="Scroll"
      aria-orientation="vertical"
      aria-valuenow={0}
      onPointerDown={(event) => { event.preventDefault(); event.currentTarget.setPointerCapture?.(event.pointerId); strip.current = event.clientY; wheelCarry.current = { x: 0, y: 0 }; }}
      onPointerMove={(event) => { if (strip.current === undefined) return; const dy = event.clientY - strip.current; strip.current = event.clientY; wheel(0, -dy * 2); }}
      onPointerUp={() => { strip.current = undefined; }}
      onPointerCancel={() => { strip.current = undefined; }}
    />
  </div>;
}

export function RemoteKeyboard({ send }: { send: (event: RemoteEventInput) => void }) {
  const inputRef = React.useRef<HTMLInputElement>(null);
  const previous = React.useRef("");
  const onChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const next = event.currentTarget.value;
    const before = previous.current;
    let common = 0;
    while (common < before.length && common < next.length && before[common] === next[common]) common += 1;
    for (let i = 0; i < before.length - common; i += 1) send({ type: "key", key: "Backspace" });
    const added = next.slice(common);
    if (added) send({ type: "text", text: added });
    previous.current = next;
  };
  const reset = () => { previous.current = ""; if (inputRef.current) inputRef.current.value = ""; };
  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter" || event.key === "Tab" || event.key === "Escape") {
      event.preventDefault();
      send({ type: "key", key: event.key });
      if (event.key !== "Tab") reset();
    } else if (event.key === "Backspace" && previous.current === "") {
      send({ type: "key", key: "Backspace" });
    }
  };
  const quick = (label: string, key: string) => <button className="remote-button" type="button" aria-label={label} onClick={() => send({ type: "key", key })}>{label}</button>;
  return <div className="remote-keyboard">
    <button className="remote-button" type="button" onClick={() => inputRef.current?.focus()}>Keyboard</button>
    <input
      ref={inputRef}
      className="remote-keyboard-input"
      aria-label="Type on screen"
      autoCapitalize="off"
      autoComplete="off"
      autoCorrect="off"
      spellCheck={false}
      onChange={onChange}
      onKeyDown={onKeyDown}
      onBlur={reset}
    />
    <div className="remote-quickkeys">
      {quick("Back", "BrowserBack")}{quick("Forward", "BrowserForward")}{quick("Reload", "BrowserReload")}{quick("Esc", "Escape")}{quick("Tab", "Tab")}
      {quick("←", "ArrowLeft")}{quick("↑", "ArrowUp")}{quick("↓", "ArrowDown")}{quick("→", "ArrowRight")}
    </div>
  </div>;
}

/** Unauthenticated phone remote: the session token travels in the URL hash and never reaches the server logs. */
export function RemoteControlPage() {
  const { sessionId = "" } = useParams({ strict: false });
  const token = React.useMemo(() => new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "", []);
  const dialTimer = React.useRef<number | undefined>(undefined);
  const [dial, setDial] = React.useState("");
  const [mode, setMode] = React.useState<"remote" | "trackpad">("remote");
  const [status, setStatus] = React.useState<"ready" | "error">(token ? "ready" : "error");
  const sendEvent = useRemoteSender(sessionId, token, setStatus);
  const send = (key: string) => sendEvent({ type: "key", key });
  React.useEffect(() => () => { if (dialTimer.current) window.clearTimeout(dialTimer.current); }, []);
  const pushDigit = (digit: number) => {
    if (dialTimer.current) window.clearTimeout(dialTimer.current);
    const next = `${dial}${digit}`.slice(-3);
    setDial(next);
    dialTimer.current = window.setTimeout(() => {
      send(`ChannelNumber:${next}`);
      setDial("");
    }, 900);
  };
  const button = (label: React.ReactNode, key: string, className = "") => <button className={`remote-button ${className}`} type="button" onClick={() => send(key)}>{label}</button>;
  return <main className="remote-shell">
    <section className="remote-body">
      <header className="remote-top"><div><p>Cuelight</p><h1>Remote</h1></div><span className={`remote-status ${status}`}>{status === "ready" ? "Connected" : "Connection error"}</span></header>
      <div className="remote-modes" role="tablist" aria-label="Remote mode">
        <button role="tab" type="button" aria-selected={mode === "remote"} className={`remote-button${mode === "remote" ? " active" : ""}`} onClick={() => setMode("remote")}>Remote</button>
        <button role="tab" type="button" aria-selected={mode === "trackpad"} className={`remote-button${mode === "trackpad" ? " active" : ""}`} onClick={() => setMode("trackpad")}>Trackpad</button>
      </div>
      {mode === "trackpad" ? <><Trackpad send={sendEvent} /><RemoteKeyboard send={sendEvent} /></> : <>
      <div className="remote-rockers">
        <div className="remote-rocker">{button("CH UP", "ChannelUp")}<span>CH</span>{button("CH DOWN", "ChannelDown")}</div>
        <div className="remote-rocker">{button("VOL UP", "VolumeUp")}<span>VOL</span>{button("VOL DOWN", "VolumeDown")}</div>
      </div>
      <div className="remote-dpad">
        {button("▲", "ArrowUp", "up")}
        {button("◀", "ArrowLeft", "left")}
        {button("OK", "Enter", "ok")}
        {button("▶", "ArrowRight", "right")}
        {button("▼", "ArrowDown", "down")}
      </div>
      <div className="remote-actions">{button("Guide", "Guide")}{button("Info", "Info")}{button("Mute", "Mute")}</div>
      <div className="remote-numpad">
        {[1, 2, 3, 4, 5, 6, 7, 8, 9].map((digit) => <button className="remote-button" key={digit} type="button" onClick={() => pushDigit(digit)}>{digit}</button>)}
        <button className="remote-button zero" type="button" onClick={() => pushDigit(0)}>0</button>
      </div>
      {dial && <div className="remote-dial" aria-live="polite">{dial}</div>}
      </>}
    </section>
  </main>;
}
