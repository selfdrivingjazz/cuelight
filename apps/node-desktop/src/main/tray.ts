import { Menu, nativeImage, Tray } from "electron";
import type { DesktopStatus } from "../shared/contracts";

export interface TrayActions {
  showControls(): void;
  showOutput(): void;
  setPinned(id: string, pinned: boolean): void;
  quit(): void;
}

/** Status-area menu: every network with on-air/standby state and a pin toggle. */
export class StatusTray {
  private tray?: Tray;

  constructor(private readonly actions: TrayActions) {}

  update(status: DesktopStatus): void {
    if (!this.tray) {
      this.tray = new Tray(trayIcon());
      this.tray.setToolTip("Cuelight");
      // Linux and Windows open the menu on click; make a plain click reach the controls too.
      this.tray.on("double-click", () => this.actions.showControls());
    }
    const networks: Electron.MenuItemConstructorOptions[] = status.networks.length
      ? status.networks.map((network) => ({
        type: "checkbox",
        checked: network.pinned,
        label: `${network.name} — ${network.onAir ? "On air" : network.standby ? "Standby" : network.phase === "running" ? "Idle" : network.phase === "error" ? "Error" : "Stopped"}${network.pinned ? " (pinned)" : ""}`,
        toolTip: "Pin to keep this network on its display regardless of newer assignments elsewhere",
        click: () => this.actions.setPinned(network.id, !network.pinned),
      }))
      : [{ label: "No networks — open Cuelight to join one", enabled: false }];
    const update = status.update;
    const updateLine: Electron.MenuItemConstructorOptions[] = update.state === "idle"
      ? []
      : [{ label: update.state === "staged" ? `Update ${update.version} ready — installs when nothing is on air` : update.state === "error" ? `Update failed: ${update.detail ?? "unknown error"}` : `Update ${update.version ?? ""}: ${update.state}${update.percent === undefined ? "" : ` ${update.percent}%`}`, enabled: false }, { type: "separator" }];
    this.tray.setContextMenu(Menu.buildFromTemplate([
      { label: "Networks (check to pin)", enabled: false },
      ...networks,
      { type: "separator" },
      ...updateLine,
      { label: "Show output", enabled: status.networks.some((network) => network.onAir), click: () => this.actions.showOutput() },
      { label: "Open Cuelight…", click: () => this.actions.showControls() },
      { type: "separator" },
      { label: `Quit Cuelight ${update.appVersion}`, click: () => this.actions.quit() },
    ]));
  }

  destroy(): void {
    this.tray?.destroy();
    this.tray = undefined;
  }
}

/** A 16pt ring drawn in code (grey, so BGRA/RGBA order does not matter; a template image on macOS). */
function trayIcon(): Electron.NativeImage {
  const size = 32;
  const pixels = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      const distance = Math.hypot(x - size / 2 + 0.5, y - size / 2 + 0.5);
      if ((distance > 9 && distance < 14) || distance < 4.5) pixels.fill(0xd8, (y * size + x) * 4, (y * size + x) * 4 + 3).writeUInt8(0xff, (y * size + x) * 4 + 3);
    }
  }
  const image = nativeImage.createFromBitmap(pixels, { width: size, height: size, scaleFactor: 2 });
  if (process.platform === "darwin") image.setTemplateImage(true);
  return image;
}
