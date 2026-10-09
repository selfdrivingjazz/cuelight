import { contextBridge, ipcRenderer } from "electron";
import type { CuelightNodeDesktopAPI, DesktopStatus } from "../shared/contracts";

const api: CuelightNodeDesktopAPI = {
  status: () => ipcRenderer.invoke("node:status"),
  previewJoin: (link) => ipcRenderer.invoke("node:preview-join", link),
  dismissJoin: () => ipcRenderer.invoke("node:dismiss-join"),
  join: (input) => ipcRenderer.invoke("node:join", input),
  joinManual: (input) => ipcRenderer.invoke("node:join-manual", input),
  rename: (id, name) => ipcRenderer.invoke("node:rename", id, name),
  remove: (id) => ipcRenderer.invoke("node:remove", id),
  setDisplay: (id, binding) => ipcRenderer.invoke("node:set-display", id, binding),
  setPinned: (id, pinned) => ipcRenderer.invoke("node:set-pinned", id, pinned),
  start: (id) => ipcRenderer.invoke("node:start", id),
  stop: (id) => ipcRenderer.invoke("node:stop", id),
  updatePreferences: (preferences) => ipcRenderer.invoke("node:preferences", preferences),
  showOutput: () => ipcRenderer.invoke("node:show-output"),
  openLogs: (id) => ipcRenderer.invoke("node:open-logs", id),
  onStatus: (listener: (status: DesktopStatus) => void) => {
    const handler = (_event: Electron.IpcRendererEvent, status: DesktopStatus) => listener(status);
    ipcRenderer.on("node:status-changed", handler);
    return () => ipcRenderer.removeListener("node:status-changed", handler);
  },
};

contextBridge.exposeInMainWorld("cuelightNode", api);
