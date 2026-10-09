import type { UpdateStatus } from "./contracts";

/** One-line update status for the setup window and tray; undefined when idle. */
export function updateLabel(update: UpdateStatus, now = Date.now()): string | undefined {
  switch (update.state) {
    case "idle": return undefined;
    case "installing": {
      const seconds = Math.max(0, Math.ceil(((update.installAt ?? now) - now) / 1000));
      return seconds > 0 ? `Updating to ${update.version} — restarting in ${seconds} s` : `Updating to ${update.version} — restarting…`;
    }
    case "error": return `Update failed: ${update.detail ?? "unknown error"}`;
    case "downloading": return `Downloading update ${update.version ?? ""}${update.percent === undefined ? "" : ` ${update.percent}%`}`;
    case "checking": return `Checking update ${update.version ?? ""}`;
  }
}
