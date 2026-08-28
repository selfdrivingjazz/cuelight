import { BrowserWindow, WebContentsView } from "electron";
import { randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { isIP } from "node:net";

interface ManagedOutput {
  id: string;
  origin: string;
}

export class RendererServer {
  private onChange: () => void = () => undefined;
  readonly token = randomBytes(32).toString("base64url");
  private server = createServer((request, response) => void this.handle(request, response));
  private fullScreen = true;
  private baseUrl?: string;
  private host?: BrowserWindow;
  private viewHost?: BrowserWindow;
  private view?: WebContentsView;
  private output?: ManagedOutput;
  private showingOutput = false;
  private readonly resizeOutput = () => this.updateBounds();

  setOnChange(listener: () => void): void { this.onChange = listener; }

  attachHost(window: BrowserWindow): void {
    if (this.host === window) return;
    if (this.host) this.detachHost(this.host);
    this.host = window;
    window.on("resize", this.resizeOutput);
    window.on("enter-full-screen", this.resizeOutput);
    window.on("leave-full-screen", this.resizeOutput);
    this.attachView();
    if (this.showingOutput) this.showOutput();
  }

  detachHost(window: BrowserWindow): void {
    if (this.host !== window) return;
    window.off("resize", this.resizeOutput);
    window.off("enter-full-screen", this.resizeOutput);
    window.off("leave-full-screen", this.resizeOutput);
    if (this.view && this.viewHost === window && !window.isDestroyed()) window.contentView.removeChildView(this.view);
    this.viewHost = undefined;
    this.host = undefined;
  }

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

  setFullScreen(value: boolean): void {
    this.fullScreen = value;
    if (this.showingOutput && this.host && !this.host.isDestroyed()) this.host.setFullScreen(value);
  }

  count(): number { return this.output ? 1 : 0; }

  showOutput(): void {
    if (!this.output) throw new Error("no active output is available");
    if (!this.host || this.host.isDestroyed()) throw new Error("desktop window is unavailable");
    this.attachView();
    const wasShowing = this.showingOutput;
    this.showingOutput = true;
    if (!wasShowing) this.host.setFullScreen(this.fullScreen);
    this.updateBounds();
    this.view?.setVisible(true);
    this.host.show();
    this.host.focus();
    this.view?.webContents.focus();
  }
  showControls(): void {
    this.showingOutput = false;
    this.view?.setVisible(false);
    if (this.host && !this.host.isDestroyed()) {
      if (this.host.isFullScreen()) this.host.setFullScreen(false);
      this.host.show();
      this.host.focus();
      this.host.webContents.focus();
    }
  }

  closeWindows(): void {
    this.output = undefined;
    this.showControls();
    if (this.view && !this.view.webContents.isDestroyed()) void this.view.webContents.loadURL("about:blank");
    this.onChange();
  }

  async close(): Promise<void> {
    this.closeWindows();
    if (this.view) {
      if (this.viewHost && !this.viewHost.isDestroyed()) this.viewHost.contentView.removeChildView(this.view);
      if (!this.view.webContents.isDestroyed()) this.view.webContents.close();
      this.view = undefined;
      this.viewHost = undefined;
    }
    if (!this.server.listening) return;
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
    this.baseUrl = undefined;
  }

  private ensureView(): WebContentsView {
    if (this.view && !this.view.webContents.isDestroyed()) return this.view;
    const view = new WebContentsView({
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true },
    });
    view.setBackgroundColor("#000000");
    view.setVisible(false);
    view.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
    view.webContents.on("will-navigate", (event, destination) => {
      try {
        if (new URL(destination).origin !== this.output?.origin) event.preventDefault();
      } catch {
        event.preventDefault();
      }
    });
    view.webContents.on("before-input-event", (event, input) => {
      if (input.key === "Escape") {
        event.preventDefault();
        this.showControls();
      }
    });
    this.view = view;
    this.attachView();
    return view;
  }

  private attachView(): void {
    if (!this.host || this.host.isDestroyed() || !this.view || this.viewHost === this.host) return;
    if (this.viewHost && !this.viewHost.isDestroyed()) this.viewHost.contentView.removeChildView(this.view);
    this.host.contentView.addChildView(this.view);
    this.viewHost = this.host;
    this.updateBounds();
  }

  private updateBounds(): void {
    if (!this.host || this.host.isDestroyed() || !this.view) return;
    const { width, height } = this.host.getContentBounds();
    this.view.setBounds({ x: 0, y: 0, width, height });
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
    if (request.method === "POST" && url.pathname === "/v1/windows") {
      const body = await this.readJSON(request, response);
      if (!body) return;
      const target = typeof body.url === "string" ? this.loopbackURL(body.url) : undefined;
      if (!target) return this.problem(response, 400, "renderer target must be a loopback HTTP URL");
      if (!this.host || this.host.isDestroyed()) return this.problem(response, 503, "desktop window is unavailable");
      const id = randomUUID();
      this.output = { id, origin: target.origin };
      const view = this.ensureView();
      this.onChange();
      try {
        await view.webContents.loadURL(target.toString());
        this.showOutput();
      } catch (error) {
        this.output = undefined;
        this.showControls();
        this.onChange();
        return this.problem(response, 502, error instanceof Error ? error.message : "renderer load failed");
      }
      return this.json(response, 201, { id });
    }
    const match = url.pathname.match(/^\/v1\/windows\/([^/]+)$/);
    const windowID = match?.[1];
    if (windowID && request.method === "GET") {
      const active = this.output?.id === decodeURIComponent(windowID) && this.view && !this.view.webContents.isDestroyed();
      return active ? this.json(response, 200, { alive: true }) : this.problem(response, 404, "renderer window not found");
    }
    if (windowID && request.method === "DELETE") {
      if (this.output?.id !== decodeURIComponent(windowID)) return this.problem(response, 404, "renderer window not found");
      this.output = undefined;
      this.onChange();
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
      if (size > 64 * 1024) {
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
