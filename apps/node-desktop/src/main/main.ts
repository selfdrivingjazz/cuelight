import { app, BrowserWindow, dialog, ipcMain, shell } from "electron";
import path from "node:path";
import { provisionInputSchema, settingsSchema, type DesktopStatus } from "../shared/contracts";
import { RendererServer } from "./renderer-server";
import { SidecarManager } from "./sidecar";

app.setName("Cable Node");
app.commandLine.appendSwitch("autoplay-policy", "no-user-gesture-required");
app.commandLine.appendSwitch("disable-features", "HardwareMediaKeyHandling");

let setupWindow: BrowserWindow | undefined;
let quitting = false;
const rendererServer = new RendererServer();
const manager = new SidecarManager(rendererServer, broadcastStatus);
rendererServer.setOnChange(() => broadcastStatus(manager.status()));

function broadcastStatus(status: DesktopStatus): void {
  if (setupWindow && !setupWindow.isDestroyed()) setupWindow.webContents.send("node:status-changed", status);
}

async function createSetupWindow(): Promise<void> {
  if (setupWindow && !setupWindow.isDestroyed()) {
    setupWindow.show();
    setupWindow.focus();
    return;
  }
  setupWindow = new BrowserWindow({
    width: 1060,
    height: 760,
    minWidth: 820,
    minHeight: 620,
    backgroundColor: "#07131f",
    title: "Cable Node",
    webPreferences: {
      preload: path.join(__dirname, "../preload/preload.js"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  setupWindow.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith("https://")) void shell.openExternal(url);
    return { action: "deny" };
  });
  setupWindow.once("closed", () => { setupWindow = undefined; });
  if (app.isPackaged) await setupWindow.loadFile(path.join(__dirname, "../../dist/index.html"));
  else await setupWindow.loadURL("http://127.0.0.1:4175");
}

function registerIPC(): void {
  ipcMain.handle("node:status", () => manager.status());
  ipcMain.handle("node:choose-ca", async () => {
    const result = await dialog.showOpenDialog({ title: "Choose controller CA certificate", properties: ["openFile"], filters: [{ name: "PEM certificate", extensions: ["pem", "crt", "cer"] }] });
    return result.canceled ? undefined : result.filePaths[0];
  });
  ipcMain.handle("node:provision", async (_event, input: unknown) => manager.provision(provisionInputSchema.parse(input)));
  ipcMain.handle("node:start", () => manager.start());
  ipcMain.handle("node:stop", () => manager.stop());
  ipcMain.handle("node:settings", async (_event, input: unknown) => manager.updateSettings(settingsSchema.parse(input)));
  ipcMain.handle("node:open-logs", () => manager.openLogs());
  ipcMain.handle("node:forget", () => manager.forget());
}

app.whenReady().then(async () => {
  registerIPC();
  await manager.initialize();
  await createSetupWindow();
  if (manager.status().settings.autoStart && manager.status().node) {
    try { await manager.start(); }
    catch (error) { dialog.showErrorBox("Cable Node could not start", error instanceof Error ? error.message : "Unknown player error"); }
  }
});

app.on("activate", () => void createSetupWindow());
app.on("window-all-closed", () => {
  // A node is a background service. Explicit Quit owns process termination.
});
app.on("before-quit", (event) => {
  if (quitting) return;
  event.preventDefault();
  quitting = true;
  void manager.stop().finally(async () => {
    await rendererServer.close();
    app.exit(0);
  });
});
