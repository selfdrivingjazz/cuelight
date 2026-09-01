import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { BrowserNodeController } from "./controller";
import { LocalCredentialStorage } from "./storage";
import type { BrowserNodeTransport, MediaPort } from "./types";
import { HttpBrowserNodeTransport } from "./transport";

class ElementMediaPort implements MediaPort {
  constructor(private readonly image: HTMLImageElement, private readonly video: HTMLVideoElement) {}
  showImage(url: string): Promise<void> {
    this.video.pause(); this.video.removeAttribute("src"); this.video.load();
    this.image.hidden = false; this.video.hidden = true;
    return new Promise((resolve, reject) => {
      this.image.onload = () => resolve();
      this.image.onerror = () => reject(new Error("image could not be observed"));
      this.image.src = url;
      if (this.image.complete && this.image.naturalWidth > 0) resolve();
    });
  }
  loadVideo(url: string): Promise<void> {
    this.image.hidden = true; this.video.hidden = false;
    return new Promise((resolve, reject) => {
      this.video.onloadeddata = () => resolve();
      this.video.onerror = () => reject(new Error("video could not be loaded"));
      this.video.src = url;
      this.video.load();
    });
  }
  playVideo(): Promise<void> { return this.video.play(); }
  clear(): void {
    this.image.hidden = true; this.video.hidden = true;
    this.image.removeAttribute("src"); this.video.pause(); this.video.removeAttribute("src"); this.video.load();
  }
}

export type WebsiteNodeAppProps = { transport?: BrowserNodeTransport; nodeId?: string };

export function WebsiteNodeApp({ transport = new HttpBrowserNodeTransport(), nodeId }: WebsiteNodeAppProps) {
  const imageRef = useRef<HTMLImageElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const [claimCode, setClaimCode] = useState("");
  const [claiming, setClaiming] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [controller] = useState(() => new BrowserNodeController(
    transport,
    new LocalCredentialStorage(),
    // The media port is replaced after mount once the actual elements exist.
    { showImage: async () => {}, loadVideo: async () => {}, playVideo: async () => {}, clear: () => {} },
  ));
  const [mediaReady, setMediaReady] = useState(false);
  const [snapshot, setSnapshot] = useState(controller.snapshot);
  const resolvedNodeId = useMemo(() => nodeId ?? new URLSearchParams(window.location.search).get("node") ?? "", [nodeId]);

  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  useEffect(() => {
    if (!resolvedNodeId || !imageRef.current || !videoRef.current) return;
    // Keep the controller instance stable while wiring the real DOM adapter.
    controller.setMediaPort(new ElementMediaPort(imageRef.current, videoRef.current));
    setMediaReady(true);
    void controller.start(resolvedNodeId);
    return () => controller.disconnect();
  }, [controller, resolvedNodeId]);

  async function claim(event: FormEvent) {
    event.preventDefault();
    setClaiming(true); setMessage(null);
    try { await controller.claim(resolvedNodeId, claimCode); setClaimCode(""); }
    catch { setMessage("That claim code could not be used."); }
    finally { setClaiming(false); }
  }

  const status = snapshot.status;
  if (!resolvedNodeId) return <main className="node-shell"><p>This browser-node URL is missing its node identity.</p></main>;
  if (!mediaReady || status === "connecting") return <main className="node-shell"><p>Connecting browser node…</p></main>;
  if (status === "unclaimed" || status === "error") return (
    <main className="node-shell claim-screen">
      <form onSubmit={claim} aria-label="Claim browser node">
        <h1>Claim this browser node</h1>
        <p>Enter the one-time code shown by your controller.</p>
        <label>Claim code<input value={claimCode} onChange={(event) => setClaimCode(event.target.value)} autoComplete="one-time-code" required /></label>
        <button type="submit" disabled={claiming}>{claiming ? "Claiming…" : "Claim node"}</button>
        {message && <p role="alert">{message}</p>}
      </form>
    </main>
  );

  return (
    <main className="node-shell" aria-label="Website node">
      <img ref={imageRef} alt="Current assignment" hidden />
      <video ref={videoRef} muted playsInline controls={false} hidden />
      {status === "autoplay_suspended" && <button className="resume" onClick={() => void controller.resumeAutoplay()}>Tap to play</button>}
      {status === "unsupported" && <p role="status">This browser cannot play the assigned media.</p>}
      {status === "offline" && <p role="status">Waiting for the controller connection.</p>}
    </main>
  );
}
