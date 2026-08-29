import * as React from "react";
import QRCode from "qrcode";

export interface RemoteEvent {
  sequence: number;
  type: "key" | "text" | "pointer" | "wheel";
  key?: string;
  text?: string;
  x?: number;
  y?: number;
  deltaX?: number;
  deltaY?: number;
  button?: string;
  state?: string;
}
function delay(milliseconds: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  window.setTimeout(resolve, milliseconds);
  return promise;
}
const cursors = new Map<string, number>();


export function RemoteQRCode({ url }: { url: string }) {
  const [source, setSource] = React.useState("");
  React.useEffect(() => {
    let active = true;
    void QRCode.toDataURL(url, { width: 220, margin: 1, color: { dark: "#07152e", light: "#f4fbff" } }).then((value) => { if (active) setSource(value); });
    return () => { active = false; };
  }, [url]);
  return source ? <aside className="qr-card"><span>Remote</span><img src={source} alt="Scan to open this node remote" /></aside> : null;
}

export function useRemoteInput(remoteURL: string | undefined, onEvent: (event: RemoteEvent) => void) {
  const handler = React.useRef(onEvent);
  React.useEffect(() => { handler.current = onEvent; }, [onEvent]);
  React.useEffect(() => {
    if (!remoteURL) return;
    const controller = new AbortController();
    let after = cursors.get(remoteURL) ?? 0;
    let initialized = cursors.has(remoteURL);
    const poll = async () => {
      while (!controller.signal.aborted) {
        try {
          const response = await fetch(`/bridge/v1/remote/events?after=${after}&waitSeconds=${initialized ? 25 : 0}`, { cache: "no-store", credentials: "omit", signal: controller.signal });
          if (!response.ok) { await delay(1_000); continue; }
          const page = await response.json() as { events?: RemoteEvent[]; nextSequence?: number };
          after = page.nextSequence ?? after;
          cursors.set(remoteURL, after);
          if (!initialized) {
            initialized = true;
            continue;
          }
          for (const event of page.events ?? []) {
            handler.current(event);
            await delay(32);
          }
        } catch {
          if (!controller.signal.aborted) await delay(1_000);
        }
      }
    };
    void poll();
    return () => controller.abort();
  }, [remoteURL]);
}
