import { app, powerSaveBlocker } from "electron";
import { spawn, type ChildProcess } from "node:child_process";
import { createWriteStream, existsSync, type WriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import type { DesktopPreferences, NetworkPhase, ProvisionedNode } from "../shared/contracts";

export interface SidecarLaunch {
  node: ProvisionedNode;
  stateDirectory: string;
  logPath: string;
  rendererUrl: string;
  rendererToken: string;
  presentationMode: DesktopPreferences["presentationMode"];
}

/** One `cuelight-player` process for one membership. */
export class Sidecar {
  phase: NetworkPhase = "stopped";
  lastError?: string;
  private child?: ChildProcess;
  private sleepBlocker?: number;

  constructor(private readonly onExit: () => void, private readonly onChange: () => void) {}

  get pid(): number | undefined { return this.child?.pid; }
  get running(): boolean { return Boolean(this.child); }

  async start(launch: SidecarLaunch): Promise<void> {
    if (this.child) return;
    this.phase = "starting";
    this.lastError = undefined;
    this.onChange();
    const { node, stateDirectory } = launch;
    const environment: NodeJS.ProcessEnv = {
      ...process.env,
      CUELIGHT_PLAYER_CONTROL_URL: node.controlUrl,
      CUELIGHT_PLAYER_NODE_ID: node.nodeId,
      // Unset means system roots (publicly trusted control chain).
      ...(node.caPath ? { CUELIGHT_PLAYER_SERVER_CA_PATH: node.caPath } : {}),
      CUELIGHT_PLAYER_NODE_CERT_PATH: node.certificatePath,
      CUELIGHT_PLAYER_NODE_KEY_PATH: node.privateKeyPath,
      CUELIGHT_PLAYER_PUBLISHER_KEY_ID: node.publisherKeyId,
      CUELIGHT_PLAYER_PUBLISHER_PUBLIC_KEY_PATH: node.publisherPublicKeyPath,
      CUELIGHT_PLAYER_EVENT_EPOCH: node.eventEpoch,
      CUELIGHT_PLAYER_PROFILE_ID: "desktop-electron",
      CUELIGHT_PLAYER_RENDERER_VERSION: `electron/${process.versions.electron}`,
      CUELIGHT_PLAYER_STATE_PATH: path.join(stateDirectory, "player.db"),
      CUELIGHT_PLAYER_CAS_PATH: path.join(stateDirectory, "cas"),
      CUELIGHT_PLAYER_RENDERER_MODE: "desktop",
      CUELIGHT_PLAYER_DESKTOP_RENDERER_URL: launch.rendererUrl,
      CUELIGHT_PLAYER_DESKTOP_RENDERER_TOKEN: launch.rendererToken,
      CUELIGHT_PLAYER_CHROMIUM_RUNTIME_ROOT: path.join(stateDirectory, "runtime"),
      CUELIGHT_PLAYER_LOOPBACK_ADDR: "127.0.0.1:0",
      CUELIGHT_PLAYER_FEATURES: "chromium,web",
      CUELIGHT_PLAYER_ACCEPTED_MEDIA_TYPES: "video/mp4,image/jpeg,image/png,image/webp",
      CUELIGHT_PLAYER_PRESENTATION_MODE: launch.presentationMode,
      CUELIGHT_PLAYER_PROBATION: "2s",
      CUELIGHT_PLAYER_UPDATE_METHOD: "desktop",
    };
    await mkdir(path.join(stateDirectory, "cas"), { recursive: true });
    await mkdir(path.dirname(launch.logPath), { recursive: true });
    const log = createWriteStream(launch.logPath, { flags: "a", mode: 0o600 });
    const child = spawn(playerBinary(), [], { env: environment, stdio: ["ignore", "pipe", "pipe"], windowsHide: true });
    this.child = child;
    child.stdout?.pipe(log, { end: false });
    child.stderr?.pipe(log, { end: false });
    child.once("spawn", () => {
      if (this.child !== child) return;
      this.phase = "running";
      this.sleepBlocker = powerSaveBlocker.start("prevent-display-sleep");
      this.onChange();
    });
    child.once("error", (error) => this.finish(child, error.message, log));
    child.once("exit", (code, signal) => this.finish(child, code === 0 || signal === "SIGTERM" ? undefined : `player exited with ${signal ?? code}`, log));
  }

  async stop(): Promise<void> {
    const child = this.child;
    if (!child) return;
    await new Promise<void>((resolve) => {
      const timeout = setTimeout(() => { child.kill("SIGKILL"); resolve(); }, 5_000);
      child.once("exit", () => { clearTimeout(timeout); resolve(); });
      child.kill("SIGTERM");
    });
  }

  private finish(child: ChildProcess, error: string | undefined, log: WriteStream): void {
    if (this.child !== child) return;
    this.child = undefined;
    log.end();
    if (this.sleepBlocker !== undefined && powerSaveBlocker.isStarted(this.sleepBlocker)) powerSaveBlocker.stop(this.sleepBlocker);
    this.sleepBlocker = undefined;
    this.lastError = error;
    this.phase = error ? "error" : "stopped";
    this.onExit();
    this.onChange();
  }
}

export function playerBinary(): string {
  const name = process.platform === "win32" ? "cuelight-player.exe" : "cuelight-player";
  const override = process.env.CUELIGHT_PLAYER_BINARY;
  if (override) return override;
  const packaged = path.join(process.resourcesPath, "bin", name);
  if (app.isPackaged || existsSync(packaged)) return packaged;
  const development = path.resolve(__dirname, "../../resources/bin", `${process.platform}-${process.arch}`, name);
  if (existsSync(development)) return development;
  return path.resolve(__dirname, "../../../../bin", name);
}

/** Runs a one-shot `cuelight-player` subcommand and returns stdout. */
export async function runPlayer(args: string[], environment: NodeJS.ProcessEnv): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    const child = spawn(playerBinary(), args, { env: { ...process.env, ...environment }, stdio: ["ignore", "pipe", "pipe"], windowsHide: true });
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
      if (size > 1 << 20) return reject(new Error("player output exceeded its limit"));
      if (code !== 0) return reject(new Error(Buffer.concat(stderr).toString("utf8").trim() || `player exited ${code}`));
      resolve(Buffer.concat(stdout).toString("utf8"));
    });
  });
}
