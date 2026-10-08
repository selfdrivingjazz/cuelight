import * as React from "react";
import QRCode from "qrcode";
import { fetchRemoteEvents, GuideAuthError, type GuideAuth } from "../hosted";

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

export interface RemoteDescriptor {
  url: string;
  showQr: boolean;
  expiresAt: string;
}

/**
 * Long-polls the node's remote session through the guide API. When the node
 * renews its remote session the response carries the new descriptor; `onRemote`
 * receives it and the cursor carries over (sequences stay monotonic).
 */
export function useRemoteInput(auth: GuideAuth | undefined, remoteURL: string | undefined, onEvent: (event: RemoteEvent) => void, onRemote?: (remote: RemoteDescriptor) => void, onAuthError?: (error: GuideAuthError) => void) {
  const handlers = React.useRef({ onEvent, onRemote, onAuthError });
  React.useEffect(() => { handlers.current = { onEvent, onRemote, onAuthError }; }, [onEvent, onRemote, onAuthError]);
  React.useEffect(() => {
    if (!auth || !remoteURL) return;
    const controller = new AbortController();
    let after = cursors.get(remoteURL) ?? 0;
    let initialized = cursors.has(remoteURL);
    const poll = async () => {
      while (!controller.signal.aborted) {
        try {
          const page = await fetchRemoteEvents(auth, after, initialized ? 25 : 0, controller.signal);
          after = page.next;
          cursors.set(remoteURL, after);
          if (page.remote?.url && page.remote.url !== remoteURL) {
            cursors.set(page.remote.url, after);
            handlers.current.onRemote?.(page.remote);
          }
          if (!initialized) {
            initialized = true;
            continue;
          }
          for (const event of page.events) {
            handlers.current.onEvent(event);
            await delay(75);
          }
        } catch (error) {
          if (error instanceof GuideAuthError) {
            handlers.current.onAuthError?.(error);
            return;
          }
          if (!controller.signal.aborted) await delay(1_000);
        }
      }
    };
    void poll();
    return () => controller.abort();
  }, [auth, remoteURL]);
}
