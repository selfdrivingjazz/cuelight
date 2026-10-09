import { app } from "electron";
import { autoUpdater, type UpdateInfo } from "electron-updater";
import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import path from "node:path";
import type { UpdateStatus } from "../shared/contracts";
import { compareVersions } from "../shared/version";

/**
 * Applies app updates handed over by a sidecar (`POST /v1/software-update`).
 * The sidecar already downloaded and verified the electron-updater feed
 * (latest*.yml + referenced files) into `feedDir`; this serves that
 * directory on loopback and lets electron-updater's generic provider install
 * it, so Electron never talks TLS to control.
 */
export class DesktopUpdater {
  private state: UpdateStatus = { appVersion: app.getVersion(), state: "idle" };
  private requested?: string;
  private staged?: string;
  private feed?: { server: Server; directory: string; url: string };
  private rollback?: () => void;

  /**
   * `prepareInstall` runs right before the installer takes over (stop sidecars,
   * release the single-instance lock so the relaunched app can start) and
   * returns how to undo that if the installer fails.
   */
  constructor(private readonly onChange: () => void, private readonly nothingOnAir: () => boolean, private readonly prepareInstall: () => Promise<() => void>) {
    autoUpdater.autoDownload = true;
    autoUpdater.autoInstallOnAppQuit = true;
    autoUpdater.allowDowngrade = false;
    // The feed is local disk; differential (Range) downloads would only add failure modes.
    autoUpdater.disableDifferentialDownload = true;
    autoUpdater.on("checking-for-update", () => this.set({ state: "checking" }));
    autoUpdater.on("update-not-available", (info: UpdateInfo) => this.set({ state: this.staged ? "staged" : "idle", version: this.staged, detail: `feed offers ${info.version}, nothing newer than ${app.getVersion()}` }));
    autoUpdater.on("download-progress", (progress: { percent: number }) => this.set({ state: "downloading", percent: Math.round(progress.percent) }));
    autoUpdater.on("update-downloaded", (info: UpdateInfo) => {
      this.staged = info.version;
      this.set({ state: "staged", version: info.version, percent: undefined, detail: "installs when nothing is on air, or at quit" });
      this.maybeInstall();
    });
    autoUpdater.on("error", (error: Error) => {
      const rollback = this.rollback;
      if (rollback) {
        // The installer failed after we stopped everything: resume, and do not retry this download.
        this.rollback = undefined;
        this.staged = undefined;
        rollback();
      }
      this.set({ state: "error", detail: error.message });
    });
  }

  status(): UpdateStatus { return this.state; }

  /** Queues `version` from `feedDir`; returns an error message when the request is malformed. */
  request(version: string, feedDir: string): string | undefined {
    const floor = [app.getVersion(), this.staged, this.requested].filter((value): value is string => Boolean(value));
    if (floor.some((current) => compareVersions(version, current) <= 0)) {
      // Not an error for the sidecar: it is simply not newer than what runs or is queued.
      return undefined;
    }
    this.requested = version;
    this.set({ state: "checking", version, percent: undefined, detail: undefined });
    void this.check(version, feedDir);
    return undefined;
  }

  /** Installs a staged update once no membership is on air. */
  maybeInstall(): void {
    if (!this.staged || this.state.state === "installing" || !this.nothingOnAir()) return;
    this.set({ state: "installing", detail: undefined });
    void this.prepareInstall().then((rollback) => {
      this.rollback = rollback;
      autoUpdater.quitAndInstall(true, true);
    });
  }

  async close(): Promise<void> {
    const feed = this.feed;
    this.feed = undefined;
    if (feed) await new Promise<void>((resolve) => feed.server.close(() => resolve()));
  }

  private async check(version: string, feedDir: string): Promise<void> {
    try {
      if (!(await stat(feedDir)).isDirectory()) throw new Error("feedDir is not a directory");
      const url = await this.serve(feedDir);
      if (this.requested !== version) return;
      autoUpdater.setFeedURL({ provider: "generic", url });
      if (!autoUpdater.isUpdaterActive()) throw new Error("updates apply only to packaged builds");
      await autoUpdater.checkForUpdates();
    } catch (error) {
      if (this.requested === version) this.requested = undefined;
      this.set({ state: "error", version, detail: error instanceof Error ? error.message : String(error) });
    }
  }

  /** Serves exactly the files directly inside `directory` on a loopback port. */
  private async serve(directory: string): Promise<string> {
    if (this.feed?.directory === directory) return this.feed.url;
    await this.close();
    const server = createServer((request, response) => {
      const name = decodeURIComponent(new URL(request.url ?? "/", "http://feed.invalid").pathname.slice(1));
      if ((request.method !== "GET" && request.method !== "HEAD") || !name || name !== path.basename(name) || name.startsWith(".")) {
        response.writeHead(404).end();
        return;
      }
      const file = path.join(directory, name);
      void stat(file).then((info) => {
        if (!info.isFile()) throw new Error("not a file");
        response.writeHead(200, { "Content-Length": String(info.size), "Cache-Control": "no-store" });
        if (request.method === "HEAD") response.end();
        else createReadStream(file).pipe(response);
      }).catch(() => response.writeHead(404).end());
    });
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", () => resolve());
    });
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("update feed did not bind TCP");
    const url = `http://127.0.0.1:${address.port}/`;
    this.feed = { server, directory, url };
    return url;
  }

  private set(next: Partial<UpdateStatus>): void {
    this.state = { ...this.state, ...next };
    this.onChange();
  }
}
