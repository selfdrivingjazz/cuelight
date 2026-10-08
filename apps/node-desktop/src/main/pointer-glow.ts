import { type BrowserWindow, WebContentsView } from "electron";
import type { Point } from "./remote-input";

const size = 56;
const idleMs = 4000;
const html = `<!doctype html><meta charset="utf-8"><style>
html,body{margin:0;width:100%;height:100%;background:transparent;overflow:hidden;pointer-events:none}
i{position:absolute;left:50%;top:50%;width:18px;height:18px;margin:-9px 0 0 -9px;border-radius:50%;
background:radial-gradient(circle,#fff 0 30%,#92ffdf 45%,rgba(146,255,223,.0) 72%);
box-shadow:0 0 14px 6px rgba(146,255,223,.75),0 0 2px 1px rgba(0,0,0,.6)}
</style><i></i>`;

/**
 * Draws where remote pointer input lands. Injected events do not move the OS
 * cursor, so without this the operator cannot see the remote pointer.
 */
export class PointerGlow {
  private view?: WebContentsView;
  private host?: BrowserWindow;
  private idle?: NodeJS.Timeout;

  /** Adds the glow to `host` above every view already attached there. */
  attach(host: BrowserWindow): void {
    const view = this.ensure();
    if (this.host && this.host !== host && !this.host.isDestroyed()) this.host.contentView.removeChildView(view);
    host.contentView.addChildView(view);
    this.host = host;
  }

  detach(): void {
    if (this.view && this.host && !this.host.isDestroyed()) this.host.contentView.removeChildView(this.view);
    this.host = undefined;
  }

  /** Centres the glow on `point` (view pixels) and hides it after a short idle period. */
  show(point: Point): void {
    if (!this.view || !this.host) return;
    this.view.setBounds({ x: Math.round(point.x - size / 2), y: Math.round(point.y - size / 2), width: size, height: size });
    this.view.setVisible(true);
    clearTimeout(this.idle);
    this.idle = setTimeout(() => this.hide(), idleMs);
  }

  hide(): void {
    clearTimeout(this.idle);
    this.idle = undefined;
    this.view?.setVisible(false);
  }

  close(): void {
    this.hide();
    this.detach();
    if (this.view && !this.view.webContents.isDestroyed()) this.view.webContents.close();
    this.view = undefined;
  }

  private ensure(): WebContentsView {
    if (this.view && !this.view.webContents.isDestroyed()) return this.view;
    const view = new WebContentsView({
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, webSecurity: true, javascript: false },
    });
    view.setBackgroundColor("#00000000");
    view.setVisible(false);
    view.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
    view.webContents.on("will-navigate", (event) => event.preventDefault());
    void view.webContents.loadURL(`data:text/html;charset=utf-8,${encodeURIComponent(html)}`);
    this.view = view;
    return view;
  }
}
