import { app, screen, shell } from "electron";
import { randomBytes } from "node:crypto";
import { mkdir, rm, unlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { DesktopPreferences, DesktopStatus, DisplayBinding, DisplayInfo, JoinInput, ManualJoinInput, Membership, NetworkStatus, ProvisionedNode, SettingsFile } from "../shared/contracts";
import { preferencesSchema } from "../shared/contracts";
import { JoinLinkError, normalizeSHA256, parseJoinLink, type JoinLink } from "../shared/join-link";
import { arbitrate, resolveDisplay, type Arbitration } from "./arbiter";
import { readJSON, type Layout } from "./layout";
import { OutputWindows } from "./output-windows";
import { nextSequence, RendererServer } from "./renderer-server";
import { runPlayer, Sidecar } from "./sidecar";
import { DesktopUpdater } from "./updates";

interface Runtime {
  membership: Membership;
  node?: ProvisionedNode;
  renderer: RendererServer;
  sidecar: Sidecar;
  displayId: number;
  assignmentChangedAt?: number;
  assignmentSeq?: number;
}

interface ProvisionRequest {
  controlUrl: string;
  token: string;
  caSha256?: string;
  name: string;
  nodeName: string;
  recover: boolean;
}

/** Owns every membership: settings, identities, sidecars, renderer endpoints, and the output arbiter. */
export class NetworkManager {
  readonly updater: DesktopUpdater;
  private settings!: SettingsFile;
  private readonly runtimes = new Map<string, Runtime>();
  private arbitration: Arbitration = { onAir: new Map(), standby: new Set() };
  private windows!: OutputWindows;
  private pendingJoin?: { raw: string; link: JoinLink };

  constructor(private readonly layout: Layout, private readonly onChange: (status: DesktopStatus) => void, private readonly showControls: () => void) {
    this.updater = new DesktopUpdater(() => this.emit(), () => this.arbitration.onAir.size === 0, async () => {
      const running = [...this.runtimes.entries()].filter(([, runtime]) => runtime.sidecar.running).map(([id]) => id);
      await Promise.all(running.map((id) => this.runtimes.get(id)!.sidecar.stop()));
      // Installers relaunch the new version immediately; it must be able to take the lock.
      app.releaseSingleInstanceLock();
      return () => {
        app.requestSingleInstanceLock();
        for (const id of running) void this.start(id).catch((error: unknown) => console.error(`restart ${id} after failed update:`, error));
      };
    });
  }

  async initialize(): Promise<void> {
    this.settings = await this.layout.loadSettings();
    this.windows = new OutputWindows(this.settings.fullScreen);
    for (const membership of this.settings.networks) await this.addRuntime(membership);
    screen.on("display-added", () => this.displaysChanged());
    screen.on("display-removed", () => this.displaysChanged());
    screen.on("display-metrics-changed", () => { this.windows.relocate(); this.emit(); });
    this.rearbitrate();
  }

  status(): DesktopStatus {
    const primary = screen.getPrimaryDisplay().id;
    const displays: DisplayInfo[] = screen.getAllDisplays().map((display, index) => ({
      id: display.id,
      label: display.label || `Display ${index + 1}`,
      primary: display.id === primary,
      width: display.size.width,
      height: display.size.height,
    }));
    const networks: NetworkStatus[] = this.settings.networks.map((membership) => {
      const runtime = this.runtimes.get(membership.id);
      return {
        id: membership.id,
        name: membership.name,
        controlUrl: membership.controlUrl,
        displayBinding: membership.displayBinding,
        displayId: runtime?.displayId,
        pinned: membership.pinned === true,
        node: runtime?.node,
        phase: runtime ? runtime.sidecar.phase : "error",
        playerPid: runtime?.sidecar.pid,
        lastError: runtime?.node ? runtime.sidecar.lastError : "node identity is missing; remove and re-join this network",
        hasOutput: Boolean(runtime?.renderer.hasOutput()),
        onAir: Boolean(runtime && this.arbitration.onAir.get(runtime.displayId) === membership.id && runtime.renderer.hasOutput()),
        standby: this.arbitration.standby.has(membership.id),
        logPath: this.layout.logFile(membership.id),
      };
    });
    const pending = this.pendingJoin;
    return {
      preferences: { autoStart: this.settings.autoStart, fullScreen: this.settings.fullScreen, presentationMode: this.settings.presentationMode },
      networks,
      displays,
      update: this.updater.status(),
      pendingJoin: pending && { link: pending.raw, name: pending.link.name, controlUrl: pending.link.controlUrl, caPinned: Boolean(pending.link.caSha256) },
      defaultNodeName: os.hostname().replace(/\.local$/, "") || "Cuelight",
    };
  }

  /** Validates a join link and holds it for the operator to confirm. */
  previewJoin(raw: string): DesktopStatus {
    this.pendingJoin = { raw, link: parseJoinLink(raw) };
    this.emit();
    return this.status();
  }

  dismissJoin(): DesktopStatus {
    this.pendingJoin = undefined;
    this.emit();
    return this.status();
  }

  async join(input: JoinInput): Promise<DesktopStatus> {
    const link = parseJoinLink(input.link);
    await this.provision({ ...link, nodeName: input.nodeName, recover: false });
    if (this.pendingJoin?.raw === input.link) this.pendingJoin = undefined;
    this.emit();
    return this.status();
  }

  async joinManual(input: ManualJoinInput): Promise<DesktopStatus> {
    let caSha256: string | undefined;
    if (input.caSha256) {
      caSha256 = normalizeSHA256(input.caSha256);
      if (!caSha256) throw new JoinLinkError("CA fingerprint must be a SHA-256 (64 hex digits)");
    }
    const controlUrl = input.controlUrl.replace(/\/+$/, "").replace(/\/api\/v1$/, "");
    const name = input.networkName || new URL(controlUrl).hostname;
    await this.provision({ controlUrl, token: input.token, caSha256, name, nodeName: input.nodeName, recover: input.recover });
    return this.status();
  }

  async rename(id: string, name: string): Promise<DesktopStatus> {
    const membership = this.membership(id);
    membership.name = name.trim().slice(0, 120) || membership.name;
    await this.layout.saveSettings(this.settings);
    this.emit();
    return this.status();
  }

  /** Stops the membership's sidecar, forgets it, and deletes its directory. */
  async remove(id: string): Promise<DesktopStatus> {
    this.membership(id);
    const runtime = this.runtimes.get(id);
    if (runtime) {
      await runtime.sidecar.stop();
      await runtime.renderer.close();
      this.runtimes.delete(id);
    }
    this.settings.networks = this.settings.networks.filter((membership) => membership.id !== id);
    await this.layout.saveSettings(this.settings);
    await rm(this.layout.networkRoot(id), { recursive: true, force: true });
    this.rearbitrate();
    return this.status();
  }

  async setDisplay(id: string, binding: DisplayBinding): Promise<DesktopStatus> {
    const membership = this.membership(id);
    membership.displayBinding = binding;
    await this.layout.saveSettings(this.settings);
    this.rearbitrate();
    return this.status();
  }

  /** A pin wins its display regardless of assignment order; at most one pin per display. */
  async setPinned(id: string, pinned: boolean): Promise<DesktopStatus> {
    const membership = this.membership(id);
    const displayId = this.runtimes.get(id)?.displayId;
    if (pinned) {
      for (const other of this.settings.networks) {
        if (other.id !== id && this.runtimes.get(other.id)?.displayId === displayId) delete other.pinned;
      }
      membership.pinned = true;
    } else {
      delete membership.pinned;
    }
    await this.layout.saveSettings(this.settings);
    this.rearbitrate();
    return this.status();
  }

  async start(id: string): Promise<DesktopStatus> {
    const runtime = this.runtimes.get(id);
    if (!runtime?.node) throw new Error("this network has no node identity; remove and re-join it");
    if (runtime.sidecar.running) return this.status();
    const rendererUrl = await runtime.renderer.start();
    await runtime.sidecar.start({
      node: runtime.node,
      stateDirectory: this.layout.state(id),
      logPath: this.layout.logFile(id),
      rendererUrl,
      rendererToken: runtime.renderer.token,
      presentationMode: this.settings.presentationMode,
    });
    this.rearbitrate();
    return this.status();
  }

  async stop(id: string): Promise<DesktopStatus> {
    await this.runtimes.get(id)?.sidecar.stop();
    this.rearbitrate();
    return this.status();
  }

  async startAll(): Promise<string[]> {
    const failures: string[] = [];
    for (const membership of this.settings.networks) {
      try { await this.start(membership.id); }
      catch (error) { failures.push(`${membership.name}: ${error instanceof Error ? error.message : String(error)}`); }
    }
    return failures;
  }

  async shutdown(): Promise<void> {
    await Promise.all([...this.runtimes.values()].map((runtime) => runtime.sidecar.stop()));
    for (const runtime of this.runtimes.values()) await runtime.renderer.close();
    this.windows.closeAll();
    await this.updater.close();
  }

  async updatePreferences(input: DesktopPreferences): Promise<DesktopStatus> {
    const next = preferencesSchema.parse(input);
    const restart = next.presentationMode !== this.settings.presentationMode
      ? [...this.runtimes.entries()].filter(([, runtime]) => runtime.sidecar.running).map(([id]) => id)
      : [];
    for (const id of restart) await this.stop(id);
    Object.assign(this.settings, next);
    this.windows.setFullScreen(next.fullScreen);
    await this.layout.saveSettings(this.settings);
    await this.updateLoginItem();
    for (const id of restart) await this.start(id);
    this.emit();
    return this.status();
  }

  showOutput(): void {
    if (!this.windows.showAll()) throw new Error("no network has output on air");
  }

  async openLogs(id: string): Promise<void> {
    this.membership(id);
    await mkdir(this.layout.logs(id), { recursive: true });
    shell.showItemInFolder(this.layout.logFile(id));
  }

  outputWindows(): Electron.BrowserWindow[] { return this.windows.all(); }

  private async provision(request: ProvisionRequest): Promise<void> {
    const controlKey = normalizeControl(request.controlUrl);
    const existing = this.settings.networks.find((membership) => normalizeControl(membership.controlUrl) === controlKey);
    if (existing) throw new Error(`this computer already belongs to ${existing.name}; remove that network first`);
    const id = randomBytes(4).toString("hex");
    const identity = this.layout.identity(id);
    await mkdir(identity, { recursive: true, mode: 0o700 });
    const args = ["provision", "--control-url", request.controlUrl, "--node-name", request.nodeName, "--output", identity];
    if (request.caSha256) args.push("--ca-sha256", request.caSha256);
    if (request.recover) args.push("--recover");
    let node: ProvisionedNode;
    try {
      const output = (await runPlayer(args, { CUELIGHT_PLAYER_PROVISION_TOKEN: request.token })).trim().split(/\r?\n/).filter(Boolean).at(-1);
      if (!output) throw new Error("player provisioning returned no identity");
      node = JSON.parse(output) as ProvisionedNode;
    } catch (error) {
      await rm(this.layout.networkRoot(id), { recursive: true, force: true });
      throw error;
    }
    const membership: Membership = { id, name: request.name, controlUrl: request.controlUrl, displayBinding: "shared" };
    this.settings.networks.push(membership);
    await this.layout.saveSettings(this.settings);
    await this.addRuntime(membership, node);
    await this.start(id);
  }

  private async addRuntime(membership: Membership, node?: ProvisionedNode): Promise<void> {
    const id = membership.id;
    await Promise.all([this.layout.identity(id), this.layout.state(id), this.layout.logs(id)].map((directory) => mkdir(directory, { recursive: true, mode: 0o700 })));
    const runtime: Runtime = {
      membership,
      node: node ?? await readJSON<ProvisionedNode>(path.join(this.layout.identity(id), "node.json")),
      displayId: screen.getPrimaryDisplay().id,
      sidecar: new Sidecar(() => runtime.renderer.closeWindows(), () => this.rearbitrate()),
      renderer: new RendererServer({
        window: () => this.windows.ensure(runtime.displayId),
        outputChanged: () => this.rearbitrate(),
        assignmentChanged: (changedAt) => {
          runtime.assignmentChangedAt = changedAt;
          runtime.assignmentSeq = nextSequence();
          this.rearbitrate();
        },
        standby: () => this.arbitration.standby.has(id),
        softwareUpdate: (version, feedDir) => {
          const resolved = path.resolve(feedDir);
          if (!resolved.startsWith(this.layout.state(id) + path.sep)) return "feedDir must be inside this network's state directory";
          return this.updater.request(version, resolved);
        },
        returnToControls: () => {
          this.windows.hide(runtime.displayId);
          this.showControls();
        },
      // The original single network keeps its web logins; new networks get isolated web storage.
      }, id === "default" ? "persist:web-pages" : `persist:web-pages-${id}`),
    };
    this.runtimes.set(id, runtime);
  }

  private displaysChanged(): void {
    this.windows.relocate();
    this.rearbitrate();
  }

  private rearbitrate(): void {
    if (!this.windows) return;
    const displayIds = screen.getAllDisplays().map((display) => display.id);
    const primary = screen.getPrimaryDisplay().id;
    for (const runtime of this.runtimes.values()) runtime.displayId = resolveDisplay(runtime.membership.displayBinding, displayIds, primary);
    const running = [...this.runtimes.values()].filter((runtime) => runtime.sidecar.running);
    this.arbitration = arbitrate(running.map((runtime) => ({
      id: runtime.membership.id,
      displayId: runtime.displayId,
      running: true,
      hasOutput: runtime.renderer.hasOutput(),
      pinned: runtime.membership.pinned === true,
      assignmentChangedAt: runtime.assignmentChangedAt,
      assignmentSeq: runtime.assignmentSeq,
      outputSeq: runtime.renderer.outputSeq,
    })));
    this.windows.apply(running.map((runtime) => ({ id: runtime.membership.id, displayId: runtime.displayId, renderer: runtime.renderer })), this.arbitration);
    this.updater.maybeInstall();
    this.emit();
  }

  private membership(id: string): Membership {
    const membership = this.settings.networks.find((candidate) => candidate.id === id);
    if (!membership) throw new Error(`unknown network ${id}`);
    return membership;
  }

  private emit(): void {
    if (this.settings) this.onChange(this.status());
  }

  /** Launch at login is one entry for the whole app, whatever the number of networks. */
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
    // AppImages run from a temporary mount; autostart must launch the image itself.
    const executable = (process.env.APPIMAGE ?? process.execPath).replaceAll("\"", "\\\"");
    await writeFile(desktopFile, `[Desktop Entry]\nType=Application\nName=Cuelight\nExec="${executable}"\nTerminal=false\nX-GNOME-Autostart-enabled=true\n`, { mode: 0o600 });
  }
}

function normalizeControl(value: string): string {
  return value.trim().replace(/\/+$/, "").replace(/\/api\/v1$/, "").toLowerCase();
}
