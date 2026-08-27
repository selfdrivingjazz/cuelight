import { BrowserWindow } from "electron";
import { randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { isIP } from "node:net";

interface ManagedWindow {
  window: BrowserWindow;
  origin: string;
}

export class RendererServer {
  private onChange: () => void = () => undefined;
  readonly token = randomBytes(32).toString("base64url");
  private readonly windows = new Map<string, ManagedWindow>();
  private server = createServer((request, response) => void this.handle(request, response));
  private fullScreen = true;
  private baseUrl?: string;
  setOnChange(listener: () => void): void { this.onChange = listener; }

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
    for (const managed of this.windows.values()) managed.window.setFullScreen(value);
  }

  count(): number { return this.windows.size; }

  closeWindows(): void {
    for (const managed of this.windows.values()) managed.window.destroy();
    this.windows.clear();
    this.onChange();
  }

  async close(): Promise<void> {
    this.closeWindows();
    if (!this.server.listening) return;
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
    this.baseUrl = undefined;
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
      const id = randomUUID();
      const output = new BrowserWindow({
        show: false,
        backgroundColor: "#000000",
        autoHideMenuBar: true,
        fullscreen: this.fullScreen,
        webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true },
      });
      output.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
      output.webContents.on("will-navigate", (event, destination) => {
        try {
          if (new URL(destination).origin !== target.origin) event.preventDefault();
        } catch {
          event.preventDefault();
        }
      });
      this.windows.set(id, { window: output, origin: target.origin });
      this.onChange();
      output.once("closed", () => { this.windows.delete(id); this.onChange(); });
      output.once("ready-to-show", () => output.show());
      try {
        await output.loadURL(target.toString());
      } catch (error) {
        output.destroy();
        this.windows.delete(id);
        return this.problem(response, 502, error instanceof Error ? error.message : "renderer load failed");
      }
      return this.json(response, 201, { id });
    }
    const match = url.pathname.match(/^\/v1\/windows\/([^/]+)$/);
    const windowID = match?.[1];
    if (windowID && request.method === "GET") {
      const managed = this.windows.get(decodeURIComponent(windowID));
      return managed ? this.json(response, 200, { alive: !managed.window.isDestroyed() }) : this.problem(response, 404, "renderer window not found");
    }
    if (windowID && request.method === "DELETE") {
      const id = decodeURIComponent(windowID);
      const managed = this.windows.get(id);
      if (!managed) return this.problem(response, 404, "renderer window not found");
      managed.window.destroy();
      this.windows.delete(id);
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
