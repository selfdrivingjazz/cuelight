import { BrowserWindow, screen } from "electron";
import type { Arbitration } from "./arbiter";
import type { RendererServer } from "./renderer-server";

export interface PlacedRenderer {
  id: string;
  displayId: number;
  renderer: RendererServer;
}

interface DisplayWindow {
  window: BrowserWindow;
  /** Escape / close hid it; the next on-air change shows it again. */
  userHidden: boolean;
  shownKey?: string;
}

/** One output window per display that hosts at least one running membership. */
export class OutputWindows {
  private readonly windows = new Map<number, DisplayWindow>();
  private placed: PlacedRenderer[] = [];
  private closing = false;

  constructor(private fullScreen: boolean) {}

  /** Window for a display, created hidden on first use. */
  ensure(displayId: number): BrowserWindow {
    const existing = this.windows.get(displayId);
    if (existing && !existing.window.isDestroyed()) return existing.window;
    const bounds = this.bounds(displayId);
    const window = new BrowserWindow({
      ...bounds,
      show: false,
      backgroundColor: "#000000",
      title: "Cuelight output",
      autoHideMenuBar: true,
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
    });
    // A committed (blank) document keeps DevTools/automation from waiting on a never-navigated page.
    void window.loadURL("about:blank");
    const relayout = () => { for (const placed of this.placed) if (placed.displayId === displayId) placed.renderer.layout(); };
    window.on("resize", relayout);
    window.on("enter-full-screen", relayout);
    window.on("leave-full-screen", relayout);
    window.on("close", (event) => {
      if (this.closing) return;
      // Closing an output hides it; the window stays so standby switching is instant.
      event.preventDefault();
      this.hide(displayId);
    });
    this.windows.set(displayId, { window, userHidden: false });
    return window;
  }

  /** Applies an arbitration: attaches views, sets on-air visibility, shows or hides windows. */
  apply(placed: PlacedRenderer[], arbitration: Arbitration): void {
    this.placed = placed;
    for (const entry of placed) {
      entry.renderer.attach(this.ensure(entry.displayId));
      entry.renderer.setOnAir(arbitration.onAir.get(entry.displayId) === entry.id && entry.renderer.hasOutput());
    }
    const used = new Set(placed.map((entry) => entry.displayId));
    for (const [displayId, state] of this.windows) {
      if (!used.has(displayId) || state.window.isDestroyed()) {
        this.destroy(displayId);
        continue;
      }
      const winner = placed.find((entry) => entry.id === arbitration.onAir.get(displayId));
      const visible = Boolean(winner?.renderer.hasOutput());
      const key = winner && visible ? `${winner.id}:${winner.renderer.outputSeq ?? ""}` : undefined;
      // A different network or a new output going on air brings a hidden output back.
      if (key !== state.shownKey) state.userHidden = false;
      state.shownKey = key;
      if (visible && !state.userHidden) this.present(state, winner!.renderer);
      else this.conceal(state);
    }
  }

  setFullScreen(value: boolean): void {
    this.fullScreen = value;
    for (const [displayId, state] of this.windows) {
      if (state.window.isDestroyed()) continue;
      if (!value) this.leaveFullScreen(state.window);
      state.window.setBounds(this.bounds(displayId));
      if (value && state.window.isVisible()) this.enterFullScreen(state.window);
    }
  }

  /** Re-shows every output that has something on air. */
  showAll(): boolean {
    let shown = false;
    for (const [displayId, state] of this.windows) {
      if (!state.shownKey || state.window.isDestroyed()) continue;
      state.userHidden = false;
      const winner = this.placed.find((entry) => entry.displayId === displayId && `${entry.id}:${entry.renderer.outputSeq ?? ""}` === state.shownKey);
      if (winner) { this.present(state, winner.renderer); shown = true; }
    }
    return shown;
  }

  hide(displayId: number): void {
    const state = this.windows.get(displayId);
    if (!state || state.window.isDestroyed()) return;
    state.userHidden = true;
    this.conceal(state);
  }

  /** Keeps windows on their displays after resolution or arrangement changes. */
  relocate(): void {
    for (const [displayId, state] of this.windows) {
      if (state.window.isDestroyed()) continue;
      const fullScreen = this.isFullScreen(state.window);
      if (fullScreen) this.leaveFullScreen(state.window);
      state.window.setBounds(this.bounds(displayId));
      if (fullScreen) this.enterFullScreen(state.window);
    }
  }

  closeAll(): void {
    this.closing = true;
    for (const displayId of [...this.windows.keys()]) this.destroy(displayId);
  }

  /** Output windows, for screenshots in diagnostics. */
  all(): BrowserWindow[] { return [...this.windows.values()].map((state) => state.window).filter((window) => !window.isDestroyed()); }

  private present(state: DisplayWindow, renderer: RendererServer): void {
    const { window } = state;
    if (!window.isVisible()) {
      window.show();
      if (this.fullScreen) this.enterFullScreen(window);
    }
    window.focus();
    renderer.focus();
  }

  private conceal(state: DisplayWindow): void {
    if (state.window.isVisible()) state.window.hide();
  }

  private destroy(displayId: number): void {
    const state = this.windows.get(displayId);
    this.windows.delete(displayId);
    if (!state || state.window.isDestroyed()) return;
    // Views belong to their memberships; detach them before the window goes away.
    for (const child of state.window.contentView.children) state.window.contentView.removeChildView(child);
    state.window.destroy();
  }

  private bounds(displayId: number): Electron.Rectangle {
    const display = screen.getAllDisplays().find((candidate) => candidate.id === displayId) ?? screen.getPrimaryDisplay();
    return this.fullScreen ? display.bounds : display.workArea;
  }

  // macOS "simple" full screen avoids a separate Space, so hiding and showing is instant.
  private enterFullScreen(window: BrowserWindow): void {
    if (process.platform === "darwin") window.setSimpleFullScreen(true);
    else window.setFullScreen(true);
  }

  private leaveFullScreen(window: BrowserWindow): void {
    if (process.platform === "darwin") window.setSimpleFullScreen(false);
    else window.setFullScreen(false);
  }

  private isFullScreen(window: BrowserWindow): boolean {
    return process.platform === "darwin" ? window.isSimpleFullScreen() : window.isFullScreen();
  }
}
