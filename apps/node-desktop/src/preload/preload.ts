import { contextBridge, ipcRenderer } from "electron";
import type { CableNodeDesktopAPI, DesktopSettings, DesktopStatus, ProvisionInput } from "../shared/contracts";

const api: CableNodeDesktopAPI = {
  status: () => ipcRenderer.invoke("node:status"),
  chooseCA: () => ipcRenderer.invoke("node:choose-ca"),
  provision: (input: ProvisionInput) => ipcRenderer.invoke("node:provision", input),
  start: () => ipcRenderer.invoke("node:start"),
  stop: () => ipcRenderer.invoke("node:stop"),
  updateSettings: (settings: DesktopSettings) => ipcRenderer.invoke("node:settings", settings),
  openLogs: () => ipcRenderer.invoke("node:open-logs"),
  onStatus: (listener: (status: DesktopStatus) => void) => {
    const handler = (_event: Electron.IpcRendererEvent, status: DesktopStatus) => listener(status);
    ipcRenderer.on("node:status-changed", handler);
    return () => ipcRenderer.removeListener("node:status-changed", handler);
  },
};

contextBridge.exposeInMainWorld("cableNode", api);
