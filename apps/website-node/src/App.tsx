import { useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";
import QRCode from "qrcode";
import { BrowserNodeApi } from "./api";
import { BrowserNodeController } from "./controller";
import type { NodeSnapshot, RenderState } from "./controller";
import { isWebItem, nextScheduleChange, scheduledItem } from "./presentation";
import type { MediaItem, Presentation, ScheduleItem } from "./presentation";
import { LocalCredentialStorage } from "./storage";
import type { ClaimStarted, RemoteEvent, RemoteSession } from "./types";

type Report = (render: RenderState) => void;

/** Injected by apps/node-android's WebView; absent in plain browsers. Update calls are absent in older shells. */
declare global {
  interface Window { CuelightAndroid?: { isAvailable(): boolean; injectInput(json: string): number; appVersion?: () => string; installUpdate?: (json: string) => boolean } }
}

export function WebsiteNodeApp({ controller: provided }: { controller?: BrowserNodeController }) {
  const [controller] = useState(() => provided || new BrowserNodeController(new BrowserNodeApi(), new LocalCredentialStorage(), { userAgent: navigator.userAgent }));
  const [snapshot, setSnapshot] = useState<NodeSnapshot>(controller.snapshot);
  const [soundBlocked, setSoundBlocked] = useState(false);
  const videoRef = useRef<HTMLVideoElement | null>(null);

  useEffect(() => {
    const unsubscribe = controller.subscribe(setSnapshot);
    controller.start();
    return () => { unsubscribe(); controller.stop(); };
  }, [controller]);

  useEffect(() => keepScreenAwake(), []);

  useEffect(() => controller.onRemoteEvent((event) => handleRemoteEvent(event, snapshot, videoRef.current)), [controller, snapshot]);

  // TVs block fullscreen and unmuted playback until a user gesture; the first
  // remote/keyboard/pointer interaction unlocks both.
  useEffect(() => {
    const unlock = () => {
      requestFullscreen();
      const video = videoRef.current;
      if (video && video.dataset.wantSound === "true") {
        video.muted = false;
        void video.play().catch(() => undefined);
        setSoundBlocked(false);
      }
    };
    window.addEventListener("keydown", unlock);
    window.addEventListener("pointerdown", unlock);
    return () => { window.removeEventListener("keydown", unlock); window.removeEventListener("pointerdown", unlock); };
  }, []);

  const view = snapshot.view;
  const report: Report = (render) => { void controller.reportRender(render); };
  return (
    <main className="node-shell">
      {view.phase === "starting" && <Message title="Starting Cuelight…" />}
      {view.phase === "pairing" && <PairingScreen claim={view.claim} />}
      {view.phase === "idle" && <Message title="Paired with Cuelight" detail="Waiting for an assignment from the controller." small={`Node ${view.nodeId}`} />}
      {view.phase === "presenting" && (
        <PresentationView
          key={`${view.assignment.desiredGeneration}`}
          presentation={view.presentation}
          report={report}
          videoRef={videoRef}
          onSoundBlocked={setSoundBlocked}
        />
      )}
      {soundBlocked && view.phase === "presenting" && <div className="hint">Press OK or tap to turn on sound</div>}
      {snapshot.remote && snapshot.remote.showQr && !(view.phase === "presenting" && view.presentation.kind === "web" && view.presentation.appAccess) && <RemoteBadge session={snapshot.remote} />}
      {snapshot.offline && <div className="offline" role="status">Reconnecting to Cuelight…</div>}
    </main>
  );
}

function Message({ title, detail, small }: { title: string; detail?: string; small?: string }) {
  return (
    <section className="message">
      <h1>{title}</h1>
      {detail && <p>{detail}</p>}
      {small && <p className="small">{small}</p>}
    </section>
  );
}

function PairingScreen({ claim }: { claim: ClaimStarted }) {
  const qr = useQrCode(claim.pairUrl || "");
  return (
    <section className="pairing" aria-label="Pair this screen">
      <div className="pairing-text">
        <p className="eyebrow">Cuelight · pair this screen</p>
        <h1>Enter this code in the Cuelight controller</h1>
        <p className="code" aria-label="Pairing code">{claim.displayCode}</p>
        <p className="instructions">Open <strong>{claim.pairUrl ? displayUrl(claim.pairUrl) : "the Cuelight controller"}</strong> on your phone or computer, or scan the QR code.</p>
        <p className="small">The code refreshes automatically every few minutes.</p>
      </div>
      {qr && <img className="qr" src={qr} alt="QR code for the pairing page" />}
    </section>
  );
}

function RemoteBadge({ session }: { session: RemoteSession }) {
  const qr = useQrCode(session.remoteUrl);
  if (!qr) return null;
  return <div className="remote-badge"><img src={qr} alt="Remote control QR code" /><span>Scan for remote</span></div>;
}

function PresentationView({ presentation, report, videoRef, onSoundBlocked }: {
  presentation: Presentation;
  report: Report;
  videoRef: React.MutableRefObject<HTMLVideoElement | null>;
  onSoundBlocked: (blocked: boolean) => void;
}) {
  if (presentation.kind === "media") return <MediaView item={presentation.item} report={report} videoRef={videoRef} onSoundBlocked={onSoundBlocked} />;
  if (presentation.kind === "media-schedule") return <ScheduleView items={presentation.items} report={report} videoRef={videoRef} onSoundBlocked={onSoundBlocked} />;
  if (presentation.kind === "web") return <WebView url={presentation.url} title={presentation.appAccess ? "Assigned app" : "Assigned website"} focus={presentation.appAccess} report={report} />;
  return <Message title="This screen can't show the assigned content" detail={presentation.reason} small={`Presentation: ${presentation.presentationKind}`} />;
}

function ScheduleView({ items, report, videoRef, onSoundBlocked }: { items: ScheduleItem[]; report: Report; videoRef: React.MutableRefObject<HTMLVideoElement | null>; onSoundBlocked: (blocked: boolean) => void }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const delay = nextScheduleChange(items, now);
    if (delay === null) return;
    // Cap the timer so clock adjustments and very long waits still converge.
    const timer = setTimeout(() => setNow(Date.now()), Math.min(Math.max(delay, 250), 60000));
    return () => clearTimeout(timer);
  }, [items, now]);
  const item = scheduledItem(items, now);
  useEffect(() => { if (!item) report({ state: "off_air" }); }, [item ? item.id : ""]);
  if (!item) return <Message title="Off air" detail="Nothing is scheduled right now." />;
  if (isWebItem(item)) return <WebView key={item.id} url={item.webUrl} title="Scheduled website" report={(state) => report({ ...state, itemId: item.id })} />;
  return <MediaView key={item.id} item={item} report={report} videoRef={videoRef} onSoundBlocked={onSoundBlocked} />;
}

function MediaView({ item, report, videoRef, onSoundBlocked }: { item: MediaItem; report: Report; videoRef: React.MutableRefObject<HTMLVideoElement | null>; onSoundBlocked: (blocked: boolean) => void }) {
  const style = mediaStyle(item);
  const failed = (message: string) => report({ state: "off_air", itemId: item.id, error: message });
  if (item.element === "image") {
    return <img className="media" style={style} src={item.url} alt="" onLoad={(event) => report({ state: "playing", itemId: item.id, width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight })} onError={() => failed("image failed to load")} />;
  }
  return (
    <video
      className="media"
      style={style}
      ref={(element) => {
        videoRef.current = element;
        if (!element) return;
        element.volume = Math.max(0, Math.min(1, item.volume / 100));
        element.dataset.wantSound = item.muted ? "false" : "true";
      }}
      src={item.url}
      autoPlay
      muted={item.muted}
      loop={item.loop}
      playsInline
      preload="auto"
      onCanPlay={(event) => {
        const video = event.currentTarget;
        if (!video.paused) return;
        video.play().catch(() => {
          // Autoplay with sound is commonly blocked until a gesture: play muted.
          video.muted = true;
          onSoundBlocked(!item.muted);
          void video.play().catch(() => failed("autoplay blocked"));
        });
      }}
      onPlaying={(event) => report({ state: "playing", itemId: item.id, width: event.currentTarget.videoWidth, height: event.currentTarget.videoHeight, positionMs: Math.round(event.currentTarget.currentTime * 1000) })}
      onPause={(event) => { if (!event.currentTarget.ended) report({ state: "paused", itemId: item.id, positionMs: Math.round(event.currentTarget.currentTime * 1000) }); }}
      onError={() => failed("video failed to load or decode")}
    />
  );
}

function WebView({ url, title, focus = false, report }: { url: string; title: string; focus?: boolean; report: Report }) {
  // Sites that send X-Frame-Options/frame-ancestors refuse to load here; the
  // browser does not expose that failure, so "playing" means the frame loaded.
  // App access pages take focus so local remote keys reach them.
  return <iframe className="web" src={url} title={title} allow="autoplay; fullscreen; encrypted-media" referrerPolicy="no-referrer" onLoad={(event) => { if (focus) event.currentTarget.contentWindow?.focus(); report({ state: "playing" }); }} />;
}

function handleRemoteEvent(event: RemoteEvent, snapshot: NodeSnapshot, video: HTMLVideoElement | null): void {
  const view = snapshot.view;
  if (view.phase !== "presenting") return;
  if (view.presentation.kind === "web") {
    // App access pages poll the remote session themselves.
    if (view.presentation.appAccess) return;
    // Cross-origin frames cannot receive synthetic input from this page. The
    // Android shell exposes a native injector; plain browsers drop the event.
    const bridge = window.CuelightAndroid;
    if (bridge && bridge.isAvailable()) bridge.injectInput(JSON.stringify({ events: [event] }));
    return;
  }
  if (!video || event.type !== "key") return;
  if (event.key === "Mute") video.muted = !video.muted;
  else if (event.key === "VolumeUp") { video.muted = false; video.volume = Math.min(1, video.volume + 0.1); }
  else if (event.key === "VolumeDown") video.volume = Math.max(0, video.volume - 0.1);
  else if (event.key === "Enter" || event.key === "Space") { if (video.paused) void video.play().catch(() => undefined); else video.pause(); }
}

function mediaStyle(item: MediaItem): CSSProperties {
  const quarter = item.orientation === 90 || item.orientation === 270;
  return {
    objectFit: item.fit === "fill" ? "cover" : "contain",
    transform: item.orientation ? `rotate(${item.orientation}deg)` : undefined,
    width: quarter ? "100vh" : "100%",
    height: quarter ? "100vw" : "100%",
  };
}

function useQrCode(text: string): string {
  const [dataUrl, setDataUrl] = useState("");
  useEffect(() => {
    let current = true;
    if (!text) { setDataUrl(""); return; }
    QRCode.toDataURL(text, { margin: 1, width: 560, errorCorrectionLevel: "M" })
      .then((value) => { if (current) setDataUrl(value); })
      .catch(() => { if (current) setDataUrl(""); });
    return () => { current = false; };
  }, [text]);
  return dataUrl;
}

function displayUrl(url: string): string {
  return url.replace(/^https?:\/\//, "");
}

function requestFullscreen(): void {
  const root = document.documentElement as HTMLElement & { webkitRequestFullscreen?: () => void };
  const doc = document as Document & { webkitFullscreenElement?: Element | null };
  if (document.fullscreenElement || doc.webkitFullscreenElement) return;
  if (root.requestFullscreen) void root.requestFullscreen().catch(() => undefined);
  else if (root.webkitRequestFullscreen) root.webkitRequestFullscreen();
}

/** Holds a screen wake lock where supported, re-acquiring after visibility changes. */
function keepScreenAwake(): () => void {
  type WakeLockSentinel = { release(): Promise<void> };
  const wakeLock = (navigator as Navigator & { wakeLock?: { request(type: "screen"): Promise<WakeLockSentinel> } }).wakeLock;
  if (!wakeLock) return () => undefined;
  let sentinel: WakeLockSentinel | null = null;
  const acquire = () => {
    if (document.visibilityState !== "visible") return;
    wakeLock.request("screen").then((value) => { sentinel = value; }).catch(() => undefined);
  };
  acquire();
  document.addEventListener("visibilitychange", acquire);
  return () => {
    document.removeEventListener("visibilitychange", acquire);
    if (sentinel) void sentinel.release().catch(() => undefined);
  };
}
