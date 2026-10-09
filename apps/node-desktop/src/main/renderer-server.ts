import { type BrowserWindow, WebContentsView } from "electron";
import { randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { isIP } from "node:net";
import path from "node:path";
import QRCode from "qrcode";
import { isVersion } from "../shared/version";
import { PointerGlow } from "./pointer-glow";
import { mapRemoteEvent, parseInputBody, parseRemoteBody, webURL, type InputAction, type Point } from "./remote-input";

type OutputKind = "bridge" | "web";

interface ManagedOutput {
  id: string;
  kind: OutputKind;
  origin: string;
  qr?: string;
}

/** The desktop side a membership's renderer endpoint reports to. */
export interface OutputHost {
  /** Window for this membership's display, created on demand. */
  window(): BrowserWindow;
  /** The output was created, replaced, or removed. */
  outputChanged(): void;
  /** The sidecar accepted a new assignment (`changedAt` in ms). */
  assignmentChanged(changedAt: number): void;
  /** Whether the display currently shows another network. */
  standby(): boolean;
  /** Queues an app update from a verified feed directory; returns an error message to reject. */
  softwareUpdate(version: string, feedDir: string): string | undefined;
  /** Physical Escape on the output. */
  returnToControls(): void;
}

const qrSize = 168;
const qrMargin = 24;
/**
 * How long a closed output's last frame stays up waiting for its replacement.
 * The player closes the old window before opening the next, so without this
 * every switch would briefly hide the output and show the desktop.
 */
const handoverMs = 3000;
let sequence = 0;
/** Process-wide monotonic counter ordering outputs and assignment reports. */
export function nextSequence(): number { return ++sequence; }

/**
 * One membership's loopback renderer endpoint (own port and bearer token) and
 * the output views it drives inside its display's window.
 */
export class RendererServer {
  readonly token = randomBytes(32).toString("base64url");
  /** Sequence of the current output's creation (see arbiter). */
  outputSeq?: number;
  private server = createServer((request, response) => void this.handle(request, response));
  private baseUrl?: string;
  private host?: BrowserWindow;
  private view?: WebContentsView;
  private viewKind?: OutputKind;
  private overlay?: WebContentsView;
  private readonly glow = new PointerGlow();
  private injecting = false;
  private readonly cursor: Point = { x: 0, y: 0 };
  private output?: ManagedOutput;
  private onAir = false;
  /** Set while a closed output's frame is held for a replacement (see handoverMs). */
  private handover?: NodeJS.Timeout;

  constructor(private readonly delegate: OutputHost, private readonly webPartition: string) {}

  async start(): Promise<string> {
    if (this.baseUrl) return this.baseUrl;
    await new Promise<void>((resolve, reject) => {
      this.server.once("error", reject);
      this.server.listen(0, "127.0.0.1", () => resolve());
    });
    const address = this.server.address();
    if (!address || typeof address === "string") throw new Error("renderer server did not bind TCP");
    this.baseUrl = `http://127.0.0.1:${address.port}`;
    return this.baseUrl;
  }

  hasOutput(): boolean { return Boolean(this.output) || this.handover !== undefined; }

  private endHandover(): void {
    if (this.handover === undefined) return;
    clearTimeout(this.handover);
    this.handover = undefined;
  }

  /** Moves this membership's views into `window` (its display's output window). */
  attach(window: BrowserWindow): void {
    if (this.host === window && !window.isDestroyed()) return;
    this.detach();
    this.host = window;
    if (this.view) window.contentView.addChildView(this.view);
    // Added after the output so it stacks on top.
    if (this.overlay) window.contentView.addChildView(this.overlay);
    if (this.view) this.glow.attach(window);
    this.layout();
  }

  /** Standby views stay loaded but hidden and muted, so switching back is instant. */
  setOnAir(onAir: boolean): void {
    this.onAir = onAir;
    if (this.view && !this.view.webContents.isDestroyed()) {
      this.view.setVisible(onAir);
      this.view.webContents.setAudioMuted(!onAir);
    }
    this.overlay?.setVisible(onAir && Boolean(this.output?.qr));
    if (!onAir) this.glow.hide();
  }

  focus(): void {
    if (this.onAir && this.view && !this.view.webContents.isDestroyed()) this.view.webContents.focus();
  }

  layout(): void {
    if (!this.host || this.host.isDestroyed()) return;
    const { width, height } = this.host.getContentBounds();
    this.view?.setBounds({ x: 0, y: 0, width, height });
    this.overlay?.setBounds({ x: Math.max(0, width - qrSize - qrMargin), y: Math.max(0, height - qrSize - qrMargin), width: qrSize, height: qrSize });
  }

  /** The sidecar stopped: drop its output. */
  closeWindows(): void {
    const had = this.hasOutput();
    this.endHandover();
    this.output = undefined;
    this.outputSeq = undefined;
    this.setOnAir(false);
    if (this.view && !this.view.webContents.isDestroyed()) void this.view.webContents.loadURL("about:blank");
    if (this.overlay && !this.overlay.webContents.isDestroyed()) void this.overlay.webContents.loadURL("about:blank");
    if (had) this.delegate.outputChanged();
  }

  async close(): Promise<void> {
    this.endHandover();
    this.output = undefined;
    this.destroyView();
    this.glow.close();
    if (this.overlay) {
      if (this.host && !this.host.isDestroyed()) this.host.contentView.removeChildView(this.overlay);
      if (!this.overlay.webContents.isDestroyed()) this.overlay.webContents.close();
      this.overlay = undefined;
    }
    this.host = undefined;
    if (!this.server.listening) return;
    this.server.closeAllConnections();
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
    this.baseUrl = undefined;
  }

  private detach(): void {
    if (this.host && !this.host.isDestroyed()) {
      if (this.view) this.host.contentView.removeChildView(this.view);
      if (this.overlay) this.host.contentView.removeChildView(this.overlay);
    }
    this.glow.detach();
    this.host = undefined;
  }

  private destroyView(): void {
    if (!this.view) return;
    if (this.host && !this.host.isDestroyed()) this.host.contentView.removeChildView(this.view);
    if (!this.view.webContents.isDestroyed()) this.view.webContents.close();
    this.view = undefined;
    this.viewKind = undefined;
  }

  private ensureView(kind: OutputKind): WebContentsView {
    if (this.view && !this.view.webContents.isDestroyed() && this.viewKind === kind) return this.view;
    // Bridge and web outputs use different session partitions, so switching kinds needs a new view.
    this.destroyView();
    const view = new WebContentsView({
      webPreferences: {
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        webSecurity: true,
        ...(kind === "web" ? { partition: this.webPartition } : {}),
      },
    });
    view.setBackgroundColor("#000000");
    view.setVisible(this.onAir);
    view.webContents.setAudioMuted(!this.onAir);
    view.webContents.setWindowOpenHandler(({ url }) => {
      // Web outputs have a single view: popups and target=_blank load in place.
      if (kind === "web" && webURL(url)) void view.webContents.loadURL(url);
      return { action: "deny" };
    });
    view.webContents.on("will-navigate", (event, destination) => {
      if (kind === "web") {
        if (!webURL(destination)) event.preventDefault();
        return;
      }
      try {
        if (new URL(destination).origin !== this.output?.origin) event.preventDefault();
      } catch {
        event.preventDefault();
      }
    });
    view.webContents.on("before-input-event", (event, input) => {
      // Only physical Escape returns to controls; remote-injected Escape reaches the page.
      if (input.key === "Escape" && !this.injecting) {
        event.preventDefault();
        this.delegate.returnToControls();
      }
    });
    this.view = view;
    this.viewKind = kind;
    const window = this.delegate.window();
    if (this.host === window && !window.isDestroyed()) {
      window.contentView.addChildView(view);
      // Keep the overlay and glow above the replacement view.
      if (this.overlay) window.contentView.addChildView(this.overlay);
      this.glow.attach(window);
      this.layout();
    } else {
      this.attach(window);
    }
    return view;
  }

  private ensureOverlay(): WebContentsView {
    if (this.overlay && !this.overlay.webContents.isDestroyed()) return this.overlay;
    const overlay = new WebContentsView({
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, javascript: false },
    });
    overlay.setBackgroundColor("#00000000");
    overlay.setVisible(false);
    overlay.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
    overlay.webContents.on("will-navigate", (event) => event.preventDefault());
    // The overlay is decorative; hand any focus straight back to the output page.
    overlay.webContents.on("focus", () => this.view?.webContents.focus());
    this.overlay = overlay;
    if (this.host && !this.host.isDestroyed()) {
      this.host.contentView.addChildView(overlay);
      this.layout();
    }
    return overlay;
  }

  private async loadOverlay(remoteURL: string): Promise<string> {
    const svg = await QRCode.toString(remoteURL, { type: "svg", margin: 2, errorCorrectionLevel: "M", color: { dark: "#000000", light: "#ffffff" } });
    const html = `<!doctype html><meta charset="utf-8"><style>html,body{margin:0;background:transparent;overflow:hidden;user-select:none;pointer-events:none}div{box-sizing:border-box;width:100vw;height:100vh;padding:8px;background:#fff;border-radius:12px;opacity:.92}svg{width:100%;height:100%;display:block}</style><div>${svg}</div>`;
    const url = `data:text/html;charset=utf-8,${encodeURIComponent(html)}`;
    await this.ensureOverlay().webContents.loadURL(url);
    return url;
  }

  private inject(actions: InputAction[]): void {
    const contents = this.view?.webContents;
    if (!contents || contents.isDestroyed()) return;
    this.injecting = true;
    try {
      for (const action of actions) {
        switch (action.kind) {
          case "mouse":
            contents.sendInputEvent({ type: action.type, x: action.x, y: action.y, button: action.button, clickCount: action.clickCount });
            break;
          case "wheel":
            contents.sendInputEvent({ type: "mouseWheel", x: action.x, y: action.y, deltaX: action.deltaX, deltaY: action.deltaY, canScroll: true });
            break;
          case "key":
            contents.sendInputEvent({ type: action.type, keyCode: action.keyCode });
            break;
          case "navigate":
            if (action.action === "back" && contents.navigationHistory.canGoBack()) contents.navigationHistory.goBack();
            else if (action.action === "forward" && contents.navigationHistory.canGoForward()) contents.navigationHistory.goForward();
            else if (action.action === "reload") contents.reload();
            break;
        }
      }
    } finally {
      this.injecting = false;
    }
  }

  private authorized(request: IncomingMessage): boolean {
    const supplied = request.headers.authorization?.replace(/^Bearer\s+/i, "") ?? "";
    const expected = Buffer.from(this.token);
    const actual = Buffer.from(supplied);
    return actual.length === expected.length && timingSafeEqual(actual, expected);
  }

  private async handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    if (!this.authorized(request)) return this.problem(response, 401, "renderer authentication required");
    const url = new URL(request.url ?? "/", "http://renderer.invalid");
    if (request.method === "GET" && url.pathname === "/v1/output") {
      return this.json(response, 200, { standby: this.delegate.standby() });
    }
    if (request.method === "POST" && url.pathname === "/v1/assignment") {
      const body = await this.readJSON(request, response);
      if (!body) return;
      const changedAt = typeof body.changedAt === "string" ? Date.parse(body.changedAt) : Number.NaN;
      if (typeof body.generation !== "string" || body.generation.length === 0 || body.generation.length > 128) return this.problem(response, 400, "assignment generation must be a non-empty string");
      if (body.releaseDigest !== null && typeof body.releaseDigest !== "string") return this.problem(response, 400, "assignment releaseDigest must be a string or null");
      if (!Number.isFinite(changedAt)) return this.problem(response, 400, "assignment changedAt must be an RFC 3339 timestamp");
      this.delegate.assignmentChanged(changedAt);
      response.writeHead(204).end();
      return;
    }
    if (request.method === "POST" && url.pathname === "/v1/software-update") {
      const body = await this.readJSON(request, response);
      if (!body) return;
      if (typeof body.version !== "string" || !isVersion(body.version)) return this.problem(response, 400, "software update version must be semantic");
      if (typeof body.feedDir !== "string" || !path.isAbsolute(body.feedDir)) return this.problem(response, 400, "software update feedDir must be an absolute path");
      const rejected = this.delegate.softwareUpdate(body.version, body.feedDir);
      if (rejected) return this.problem(response, 400, rejected);
      return this.json(response, 202, { accepted: true });
    }
    if (request.method === "POST" && url.pathname === "/v1/windows") {
      const body = await this.readJSON(request, response);
      if (!body) return;
      const kind = body.kind ?? "bridge";
      if (kind !== "bridge" && kind !== "web") return this.problem(response, 400, "renderer kind must be bridge or web");
      let target: URL | undefined;
      if (kind === "web") {
        target = typeof body.url === "string" ? webURL(body.url) : undefined;
        if (!target) return this.problem(response, 400, "web target must be an absolute HTTP(S) URL without credentials");
      } else {
        target = typeof body.url === "string" ? this.loopbackURL(body.url) : undefined;
        if (!target) return this.problem(response, 400, "renderer target must be a loopback HTTP URL");
      }
      let remoteURL: string | undefined;
      if (body.remote !== undefined) {
        const remote = parseRemoteBody(body.remote);
        if (typeof remote === "string") return this.problem(response, 400, remote);
        remoteURL = remote.qrURL;
      }
      const id = randomUUID();
      // A replacement arriving during handover takes over the held frame in place.
      this.endHandover();
      this.output = { id, kind, origin: target.origin };
      const view = this.ensureView(kind);
      this.cursor.x = 0;
      this.cursor.y = 0;
      this.glow.hide();
      try {
        if (remoteURL) this.output.qr = await this.loadOverlay(remoteURL);
        else this.overlay?.setVisible(false);
        await view.webContents.loadURL(target.toString());
      } catch (error) {
        if (this.output?.id === id) {
          this.output = undefined;
          this.outputSeq = undefined;
          this.delegate.outputChanged();
        }
        return this.problem(response, 502, error instanceof Error ? error.message : "renderer load failed");
      }
      // Arbitrate only once the page is loaded, so a winning switch is never blank.
      this.outputSeq = nextSequence();
      this.setOnAir(this.onAir);
      this.delegate.outputChanged();
      return this.json(response, 201, { id });
    }
    const screenshotMatch = url.pathname.match(/^\/v1\/windows\/([^/]+)\/screenshot$/);
    const screenshotWindowID = screenshotMatch?.[1];
    if (screenshotWindowID && request.method === "GET") {
      if (this.output?.id !== decodeURIComponent(screenshotWindowID) || !this.view || this.view.webContents.isDestroyed()) {
        return this.problem(response, 404, "renderer window not found");
      }
      try {
        const png = (await this.view.webContents.capturePage()).toPNG();
        if (png.length === 0) return this.problem(response, 502, "renderer produced an empty screenshot");
        if (png.length > 16 * 1024 * 1024) return this.problem(response, 413, "renderer screenshot is too large");
        response.writeHead(200, {
          "Content-Type": "image/png",
          "Content-Length": String(png.length),
          "Cache-Control": "no-store",
        });
        response.end(png);
        return;
      } catch (error) {
        return this.problem(response, 502, error instanceof Error ? error.message : "renderer screenshot failed");
      }
    }
    const inputMatch = url.pathname.match(/^\/v1\/windows\/([^/]+)\/input$/);
    const inputWindowID = inputMatch?.[1];
    if (inputWindowID && request.method === "POST") {
      if (this.output?.id !== decodeURIComponent(inputWindowID) || !this.view || this.view.webContents.isDestroyed()) {
        return this.problem(response, 404, "renderer window not found");
      }
      const body = await this.readJSON(request, response);
      if (!body) return;
      const events = parseInputBody(body);
      if (typeof events === "string") return this.problem(response, 400, events);
      const { width, height } = this.view.getBounds();
      const web = this.output.kind === "web";
      // Bridge outputs handle remote keys themselves; only web outputs accept navigation shortcuts.
      const actions = events.flatMap((event) => mapRemoteEvent(event, { width, height }, this.cursor)).filter((action) => web || action.kind !== "navigate");
      this.inject(actions);
      // Injected input does not move the OS cursor, so draw where it lands.
      if (web && this.onAir && actions.some((action) => action.kind === "mouse" || action.kind === "wheel")) this.glow.show(this.cursor);
      response.writeHead(204).end();
      return;
    }
    const remoteMatch = url.pathname.match(/^\/v1\/windows\/([^/]+)\/remote$/);
    const remoteWindowID = remoteMatch?.[1];
    if (remoteWindowID && request.method === "POST") {
      if (this.output?.id !== decodeURIComponent(remoteWindowID)) return this.problem(response, 404, "renderer window not found");
      const body = await this.readJSON(request, response);
      if (!body) return;
      const remote = parseRemoteBody(body);
      if (typeof remote === "string") return this.problem(response, 400, remote);
      const output = this.output;
      try {
        output.qr = remote.qrURL ? await this.loadOverlay(remote.qrURL) : undefined;
      } catch (error) {
        return this.problem(response, 502, error instanceof Error ? error.message : "renderer remote update failed");
      }
      // The window may have been replaced while the overlay loaded.
      if (this.output === output) this.overlay?.setVisible(this.onAir && Boolean(output.qr));
      response.writeHead(204).end();
      return;
    }
    const match = url.pathname.match(/^\/v1\/windows\/([^/]+)$/);
    const windowID = match?.[1];
    if (windowID && request.method === "GET") {
      const active = this.output?.id === decodeURIComponent(windowID) && this.view && !this.view.webContents.isDestroyed();
      return active ? this.json(response, 200, { alive: true }) : this.problem(response, 404, "renderer window not found");
    }
    if (windowID && request.method === "DELETE") {
      if (this.output?.id !== decodeURIComponent(windowID)) return this.problem(response, 404, "renderer window not found");
      // The window is gone for the player at once; its frame stays up briefly for a replacement.
      this.output = undefined;
      this.endHandover();
      this.handover = setTimeout(() => {
        this.handover = undefined;
        if (this.output) return;
        this.outputSeq = undefined;
        this.delegate.outputChanged();
      }, handoverMs);
      response.writeHead(204).end();
      return;
    }
    this.problem(response, 404, "renderer route not found");
  }

  private loopbackURL(value: string): URL | undefined {
    try {
      const parsed = new URL(value);
      if (parsed.protocol !== "http:" || parsed.username || parsed.password) return undefined;
      const host = parsed.hostname.replace(/^\[|\]$/g, "");
      if (isIP(host) === 0 || (host !== "127.0.0.1" && host !== "::1")) return undefined;
      return parsed;
    } catch {
      return undefined;
    }
  }

  private async readJSON(request: IncomingMessage, response: ServerResponse): Promise<Record<string, unknown> | undefined> {
    const chunks: Buffer[] = [];
    let size = 0;
    for await (const chunk of request) {
      const value = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
      size += value.length;
      if (size > 256 * 1024) {
        this.problem(response, 413, "renderer request is too large");
        return undefined;
      }
      chunks.push(value);
    }
    try {
      const value: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
      this.problem(response, 400, "renderer request must be a JSON object");
      return undefined;
    } catch {
      this.problem(response, 400, "renderer request is invalid JSON");
      return undefined;
    }
  }

  private json(response: ServerResponse, status: number, body: unknown): void {
    response.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
    response.end(JSON.stringify(body));
  }

  private problem(response: ServerResponse, status: number, detail: string): void {
    this.json(response, status, { status, detail });
  }
}
