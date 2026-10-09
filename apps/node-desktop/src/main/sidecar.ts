import { app, powerSaveBlocker, shell } from "electron";
import { spawn, type ChildProcess } from "node:child_process";
import { createWriteStream, existsSync, type WriteStream } from "node:fs";
import { mkdir, readFile, rename, rm, unlink, writeFile } from "node:fs/promises";
import path from "node:path";
import type { DesktopSettings, DesktopStatus, ProvisionInput, ProvisionedNode } from "../shared/contracts";
import { settingsSchema } from "../shared/contracts";
import { RendererServer } from "./renderer-server";

export class SidecarManager {
  private child?: ChildProcess;
  private node?: ProvisionedNode;
  private phase: DesktopStatus["phase"] = "unprovisioned";
  private lastError?: string;
  private settings: DesktopSettings = { autoStart: false, fullScreen: true, presentationMode: "automatic" };
  private sleepBlocker?: number;
  private rendererUrl?: string;

  constructor(private readonly renderer: RendererServer, private readonly onChange: (status: DesktopStatus) => void) {}

  async initialize(): Promise<void> {
    await Promise.all([mkdir(this.identityDirectory(), { recursive: true }), mkdir(this.stateDirectory(), { recursive: true }), mkdir(this.logDirectory(), { recursive: true })]);
    this.node = await this.readJSON<ProvisionedNode>(path.join(this.identityDirectory(), "node.json"));
    const storedSettings = await this.readJSON<unknown>(this.settingsPath());
    const parsed = settingsSchema.safeParse(storedSettings);
    if (parsed.success) this.settings = parsed.data;
    this.renderer.setFullScreen(this.settings.fullScreen);
    this.phase = this.node ? "stopped" : "unprovisioned";
    this.emit();
  }

  status(): DesktopStatus {
    return {
      phase: this.phase,
      node: this.node,
      playerPid: this.child?.pid,
      outputWindows: this.renderer.count(),
      rendererUrl: this.rendererUrl,
      lastError: this.lastError,
      settings: this.settings,
      logPath: this.logPath(),
    };
  }

  async provision(input: ProvisionInput): Promise<DesktopStatus> {
    if (this.child) throw new Error("stop the node before changing its identity");
    const binary = this.binaryPath();
    const args = ["provision", "--control-url", input.controllerUrl, "--ca-file", input.caPath, "--node-name", input.nodeName, "--output", this.identityDirectory()];
    if (input.recover) args.push("--recover");
    const result = await this.runOnce(binary, args, { CUELIGHT_PLAYER_PROVISION_TOKEN: input.token });
    const output = result.trim().split(/\r?\n/).filter(Boolean).at(-1);
    if (!output) throw new Error("player provisioning returned no identity");
    this.node = JSON.parse(output) as ProvisionedNode;
    this.phase = "stopped";
    this.lastError = undefined;
    this.emit();
    return this.status();
  }

  async start(): Promise<DesktopStatus> {
    if (this.child) return this.status();
    if (!this.node) throw new Error("enroll or recover this node first");
    this.phase = "starting";
    this.lastError = undefined;
    this.emit();
    this.rendererUrl = await this.renderer.start();
    this.renderer.setFullScreen(this.settings.fullScreen);
    const environment: NodeJS.ProcessEnv = {
      ...process.env,
      CUELIGHT_PLAYER_CONTROL_URL: this.node.controlUrl,
      CUELIGHT_PLAYER_NODE_ID: this.node.nodeId,
      CUELIGHT_PLAYER_SERVER_CA_PATH: this.node.caPath,
      CUELIGHT_PLAYER_NODE_CERT_PATH: this.node.certificatePath,
      CUELIGHT_PLAYER_NODE_KEY_PATH: this.node.privateKeyPath,
      CUELIGHT_PLAYER_PUBLISHER_KEY_ID: this.node.publisherKeyId,
      CUELIGHT_PLAYER_PUBLISHER_PUBLIC_KEY_PATH: this.node.publisherPublicKeyPath,
      CUELIGHT_PLAYER_EVENT_EPOCH: this.node.eventEpoch,
      CUELIGHT_PLAYER_PROFILE_ID: "desktop-electron",
      CUELIGHT_PLAYER_RENDERER_VERSION: `electron/${process.versions.electron}`,
      CUELIGHT_PLAYER_STATE_PATH: path.join(this.stateDirectory(), "player.db"),
      CUELIGHT_PLAYER_CAS_PATH: path.join(this.stateDirectory(), "cas"),
      CUELIGHT_PLAYER_RENDERER_MODE: "desktop",
      CUELIGHT_PLAYER_DESKTOP_RENDERER_URL: this.rendererUrl,
      CUELIGHT_PLAYER_DESKTOP_RENDERER_TOKEN: this.renderer.token,
      CUELIGHT_PLAYER_CHROMIUM_RUNTIME_ROOT: path.join(this.stateDirectory(), "runtime"),
      CUELIGHT_PLAYER_LOOPBACK_ADDR: "127.0.0.1:0",
      CUELIGHT_PLAYER_FEATURES: "chromium,web",
      CUELIGHT_PLAYER_ACCEPTED_MEDIA_TYPES: "video/mp4,image/jpeg,image/png,image/webp",
      CUELIGHT_PLAYER_PRESENTATION_MODE: this.settings.presentationMode,
      CUELIGHT_PLAYER_PROBATION: "2s",
    };
    await mkdir(path.join(this.stateDirectory(), "cas"), { recursive: true });
    const log = createWriteStream(this.logPath(), { flags: "a", mode: 0o600 });
    const child = spawn(this.binaryPath(), [], { env: environment, stdio: ["ignore", "pipe", "pipe"], windowsHide: true });
    this.child = child;
    child.stdout?.pipe(log, { end: false });
    child.stderr?.pipe(log, { end: false });
    child.once("spawn", () => {
      if (this.child !== child) return;
      this.phase = "running";
      this.sleepBlocker = powerSaveBlocker.start("prevent-display-sleep");
      this.emit();
    });
    child.once("error", (error) => this.finishChild(child, error.message, log));
    child.once("exit", (code, signal) => this.finishChild(child, code === 0 || signal === "SIGTERM" ? undefined : `player exited with ${signal ?? code}`, log));
    return this.status();
  }

  async stop(): Promise<DesktopStatus> {
    const child = this.child;
    if (!child) {
      this.phase = this.node ? "stopped" : "unprovisioned";
      this.emit();
      return this.status();
    }
    await new Promise<void>((resolve) => {
      const timeout = setTimeout(() => { child.kill("SIGKILL"); resolve(); }, 5_000);
      child.once("exit", () => { clearTimeout(timeout); resolve(); });
      child.kill("SIGTERM");
    });
    if (this.child === child) this.child = undefined;
    this.phase = this.node ? "stopped" : "unprovisioned";
    this.stopSleepBlocker();
    this.emit();
    return this.status();
  }

  async forget(): Promise<DesktopStatus> {
    await this.stop();
    await Promise.all([
      rm(this.identityDirectory(), { recursive: true, force: true }),
      rm(this.stateDirectory(), { recursive: true, force: true }),
    ]);
    await Promise.all([
      mkdir(this.identityDirectory(), { recursive: true }),
      mkdir(this.stateDirectory(), { recursive: true }),
    ]);
    this.node = undefined;
    this.phase = "unprovisioned";
    this.lastError = undefined;
    this.emit();
    return this.status();
  }

  async updateSettings(settings: DesktopSettings): Promise<DesktopStatus> {
    const next = settingsSchema.parse(settings);
    const restart = Boolean(this.child) && next.presentationMode !== this.settings.presentationMode;
    if (restart) await this.stop();
    this.settings = next;
    this.renderer.setFullScreen(this.settings.fullScreen);
    await this.atomicJSON(this.settingsPath(), this.settings);
    await this.updateLoginItem();
    if (restart) return this.start();
    this.emit();
    return this.status();
  }

  async openLogs(): Promise<void> { await shell.showItemInFolder(this.logPath()); }

  private finishChild(child: ChildProcess, error: string | undefined, log: WriteStream): void {
    if (this.child !== child) return;
    this.child = undefined;
    log.end();
    this.renderer.closeWindows();
    this.stopSleepBlocker();
    this.lastError = error;
    this.phase = error ? "error" : this.node ? "stopped" : "unprovisioned";
    this.emit();
  }

  private stopSleepBlocker(): void {
    if (this.sleepBlocker !== undefined && powerSaveBlocker.isStarted(this.sleepBlocker)) powerSaveBlocker.stop(this.sleepBlocker);
    this.sleepBlocker = undefined;
  }

  private emit(): void { this.onChange(this.status()); }
  private async updateLoginItem(): Promise<void> {
    if (process.platform === "darwin" || process.platform === "win32") {
      app.setLoginItemSettings({ openAtLogin: this.settings.autoStart });
      return;
    }
    if (process.platform !== "linux") return;
    const autostartDirectory = path.join(app.getPath("appData"), "autostart");
    const desktopFile = path.join(autostartDirectory, "cuelight-node.desktop");
    if (!this.settings.autoStart) {
      try { await unlink(desktopFile); }
      catch (error) { if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error; }
      return;
    }
    await mkdir(autostartDirectory, { recursive: true });
    const executable = process.execPath.replaceAll("\\\"", "\\\\\"");
    await writeFile(desktopFile, `[Desktop Entry]\nType=Application\nName=Cuelight\nExec=\"${executable}\"\nTerminal=false\nX-GNOME-Autostart-enabled=true\n`, { mode: 0o600 });
  }

  private root(): string { return app.getPath("userData"); }
  private identityDirectory(): string { return path.join(this.root(), "identity"); }
  private stateDirectory(): string { return path.join(this.root(), "state"); }
  private logDirectory(): string { return path.join(this.root(), "logs"); }
  private logPath(): string { return path.join(this.logDirectory(), "player.log"); }
  private settingsPath(): string { return path.join(this.root(), "settings.json"); }

  private binaryPath(): string {
    const name = process.platform === "win32" ? "cuelight-player.exe" : "cuelight-player";
    const override = process.env.CUELIGHT_PLAYER_BINARY;
    if (override) return override;
    const packaged = path.join(process.resourcesPath, "bin", name);
    if (app.isPackaged || existsSync(packaged)) return packaged;
    const development = path.resolve(__dirname, "../../resources/bin", `${process.platform}-${process.arch}`, name);
    if (existsSync(development)) return development;
    return path.resolve(__dirname, "../../../../bin", name);
  }

  private async runOnce(binary: string, args: string[], environment: NodeJS.ProcessEnv): Promise<string> {
    return new Promise<string>((resolve, reject) => {
      const child = spawn(binary, args, { env: { ...process.env, ...environment }, stdio: ["ignore", "pipe", "pipe"], windowsHide: true });
      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      let size = 0;
      const collect = (target: Buffer[]) => (chunk: Buffer) => {
        size += chunk.length;
        if (size <= 1 << 20) target.push(chunk);
        else child.kill("SIGKILL");
      };
      child.stdout.on("data", collect(stdout));
      child.stderr.on("data", collect(stderr));
      child.once("error", reject);
      child.once("exit", (code) => {
        if (size > 1 << 20) return reject(new Error("player provisioning output exceeded its limit"));
        if (code !== 0) return reject(new Error(Buffer.concat(stderr).toString("utf8").trim() || `player provisioning exited ${code}`));
        resolve(Buffer.concat(stdout).toString("utf8"));
      });
    });
  }

  private async readJSON<T>(file: string): Promise<T | undefined> {
    try { return JSON.parse(await readFile(file, "utf8")) as T; }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
      throw error;
    }
  }

  private async atomicJSON(file: string, value: unknown): Promise<void> {
    const temporary = file + ".tmp";
    await writeFile(temporary, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });
    await rename(temporary, file);
  }
}
