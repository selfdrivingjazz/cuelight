import { app, BrowserWindow, dialog, ipcMain, shell } from "electron";
import path from "node:path";
import { displayBindingSchema, joinInputSchema, manualJoinSchema, membershipIdSchema, preferencesSchema, type DesktopStatus } from "../shared/contracts";
import { Layout } from "./layout";
import { NetworkManager } from "./networks";
import { StatusTray } from "./tray";

const protocolScheme = "cuelight";

app.setName("Cuelight");
app.commandLine.appendSwitch("autoplay-policy", "no-user-gesture-required");
app.commandLine.appendSwitch("disable-features", "HardwareMediaKeyHandling");

// One running app owns every network; later launches (including cuelight:// handoffs) forward to it.
if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  run();
}

function run(): void {
  let setupWindow: BrowserWindow | undefined;
  let quitting = false;
  const pendingLinks: string[] = [];
  const tray = new StatusTray({
    showControls: () => void createSetupWindow(),
    showOutput: () => { try { manager.showOutput(); } catch { /* nothing on air */ } },
    setPinned: (id, pinned) => void manager.setPinned(id, pinned).catch((error: unknown) => reportError("Pin failed", error)),
    quit: () => app.quit(),
  });
  const manager = new NetworkManager(new Layout(app.getPath("userData")), broadcastStatus, () => void createSetupWindow());

  function broadcastStatus(status: DesktopStatus): void {
    if (setupWindow && !setupWindow.isDestroyed()) setupWindow.webContents.send("node:status-changed", status);
    if (app.isReady()) tray.update(status);
  }

  function reportError(title: string, error: unknown): void {
    dialog.showErrorBox(title, error instanceof Error ? error.message : String(error));
  }

  async function createSetupWindow(): Promise<void> {
    if (setupWindow && !setupWindow.isDestroyed()) {
      setupWindow.show();
      setupWindow.focus();
      return;
    }
    setupWindow = new BrowserWindow({
      width: 1060,
      height: 800,
      minWidth: 820,
      minHeight: 620,
      backgroundColor: "#07131f",
      title: "Cuelight",
      webPreferences: {
        preload: path.join(__dirname, "../preload/preload.js"),
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
      },
    });
    const window = setupWindow;
    window.webContents.setWindowOpenHandler(({ url }) => {
      if (url.startsWith("https://")) void shell.openExternal(url);
      return { action: "deny" };
    });
    window.once("closed", () => { if (setupWindow === window) setupWindow = undefined; });
    if (app.isPackaged) await window.loadFile(path.join(__dirname, "../../dist/index.html"));
    else await window.loadURL(process.env.CUELIGHT_DESKTOP_DEV_URL ?? "http://127.0.0.1:4175");
  }

  /** Routes a cuelight:// link to the setup UI for confirmation. */
  function openLink(link: string): void {
    if (!app.isReady()) {
      pendingLinks.push(link);
      return;
    }
    void createSetupWindow().then(() => {
      try { manager.previewJoin(link); }
      catch (error) { reportError("Cuelight could not use that link", error); }
    });
  }

  const linkFrom = (argv: readonly string[]) => argv.find((value) => value.toLowerCase().startsWith(`${protocolScheme}:`));

  app.on("second-instance", (_event, argv) => {
    const link = linkFrom(argv);
    if (link) openLink(link);
    else void createSetupWindow();
  });
  // macOS delivers links through open-url, possibly before ready.
  app.on("open-url", (event, url) => {
    event.preventDefault();
    openLink(url);
  });
  const launchLink = linkFrom(process.argv);
  if (launchLink) pendingLinks.push(launchLink);

  // In development the protocol client is Electron itself, launched with this app's path.
  if (process.defaultApp && process.argv[1]) app.setAsDefaultProtocolClient(protocolScheme, process.execPath, [path.resolve(process.argv[1])]);
  else app.setAsDefaultProtocolClient(protocolScheme);

  function registerIPC(): void {
    const id = (value: unknown) => membershipIdSchema.parse(value);
    ipcMain.handle("node:status", () => manager.status());
    ipcMain.handle("node:preview-join", (_event, link: unknown) => manager.previewJoin(String(link)));
    ipcMain.handle("node:dismiss-join", () => manager.dismissJoin());
    ipcMain.handle("node:join", (_event, input: unknown) => manager.join(joinInputSchema.parse(input)));
    ipcMain.handle("node:join-manual", (_event, input: unknown) => manager.joinManual(manualJoinSchema.parse(input)));
    ipcMain.handle("node:rename", (_event, network: unknown, name: unknown) => manager.rename(id(network), String(name)));
    ipcMain.handle("node:remove", (_event, network: unknown) => manager.remove(id(network)));
    ipcMain.handle("node:set-display", (_event, network: unknown, binding: unknown) => manager.setDisplay(id(network), displayBindingSchema.parse(binding)));
    ipcMain.handle("node:set-pinned", (_event, network: unknown, pinned: unknown) => manager.setPinned(id(network), pinned === true));
    ipcMain.handle("node:start", (_event, network: unknown) => manager.start(id(network)));
    ipcMain.handle("node:stop", (_event, network: unknown) => manager.stop(id(network)));
    ipcMain.handle("node:preferences", (_event, input: unknown) => manager.updatePreferences(preferencesSchema.parse(input)));
    ipcMain.handle("node:open-logs", (_event, network: unknown) => manager.openLogs(id(network)));
    ipcMain.handle("node:show-output", () => manager.showOutput());
  }

  app.whenReady().then(async () => {
    registerIPC();
    await manager.initialize();
    await createSetupWindow();
    tray.update(manager.status());
    if (manager.status().preferences.autoStart) {
      const failures = await manager.startAll();
      if (failures.length) dialog.showErrorBox("Some networks could not start", failures.join("\n"));
    }
    for (const link of pendingLinks.splice(0)) openLink(link);
  }).catch((error: unknown) => {
    reportError("Cuelight could not start", error);
    app.exit(1);
  });

  app.on("activate", () => void createSetupWindow());
  app.on("window-all-closed", () => {
    // A node is a background service. Explicit Quit owns process termination.
  });
  app.on("before-quit", (event) => {
    if (quitting) return;
    event.preventDefault();
    quitting = true;
    void manager.shutdown().finally(() => {
      tray.destroy();
      // Quit for real (not app.exit) so electron-updater's quit hook can install a staged update.
      app.quit();
    });
  });
  // Session logout and service managers send SIGTERM; shut sidecars down like an explicit Quit.
  process.on("SIGTERM", () => app.quit());
}
