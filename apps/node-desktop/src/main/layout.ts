import { mkdir, readFile, readdir, rename, rmdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { settingsFileSchema, type Membership, type ProvisionedNode, type SettingsFile } from "../shared/contracts";

/** Filesystem layout under Electron's userData directory. */
export class Layout {
  constructor(readonly root: string) {}

  settingsPath(): string { return path.join(this.root, "settings.json"); }
  networksRoot(): string { return path.join(this.root, "networks"); }
  networkRoot(id: string): string { return path.join(this.networksRoot(), id); }
  identity(id: string): string { return path.join(this.networkRoot(id), "identity"); }
  state(id: string): string { return path.join(this.networkRoot(id), "state"); }
  logs(id: string): string { return path.join(this.networkRoot(id), "logs"); }
  logFile(id: string): string { return path.join(this.logs(id), "player.log"); }

  async loadSettings(): Promise<SettingsFile> {
    const stored = await readJSON<Record<string, unknown>>(this.settingsPath());
    if (stored && Array.isArray(stored.networks)) return settingsFileSchema.parse(stored);
    // Settings without a membership list predate multi-network: migrate once.
    const migrated = await this.migrateLegacy();
    const settings = settingsFileSchema.parse({ ...(stored ?? {}), networks: migrated ? [migrated] : [] });
    await this.saveSettings(settings);
    return settings;
  }

  async saveSettings(settings: SettingsFile): Promise<void> {
    await mkdir(this.root, { recursive: true });
    await atomicJSON(this.settingsPath(), settingsFileSchema.parse(settings));
  }

  /**
   * Moves the single-network `identity/`, `state/`, `logs/` into
   * `networks/default/` and rewrites node.json paths. Returns the resulting
   * membership, or undefined when there was no provisioned legacy identity.
   */
  private async migrateLegacy(): Promise<Membership | undefined> {
    const legacy = { identity: path.join(this.root, "identity"), state: path.join(this.root, "state"), logs: path.join(this.root, "logs") };
    const id = "default";
    const nodePath = path.join(this.identity(id), "node.json");
    // A destination node.json means an earlier migration was interrupted after moving identity/.
    if (await readJSON<ProvisionedNode>(path.join(legacy.identity, "node.json")) || await readJSON<ProvisionedNode>(nodePath)) {
      await mkdir(this.networkRoot(id), { recursive: true, mode: 0o700 });
      for (const [name, from] of Object.entries(legacy)) {
        try { await rename(from, path.join(this.networkRoot(id), name)); }
        catch (error) { if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error; }
      }
    } else {
      // Unprovisioned installs only ever created empty directories.
      for (const directory of Object.values(legacy)) await removeEmptyDirectory(directory);
    }
    // Read from the destination: relocation is idempotent, so an interrupted run resumes here.
    const node = await readJSON<ProvisionedNode>(nodePath);
    if (!node) return undefined;
    const relocated = relocateNode(node, legacy.identity, this.identity(id));
    await atomicJSON(nodePath, relocated);
    // Memberships store the control origin, as join links carry it; node.json keeps the API base.
    const controlUrl = relocated.controlUrl.replace(/\/+$/, "").replace(/\/api\/v1$/, "");
    let name = controlUrl;
    try { name = new URL(controlUrl).hostname; } catch { /* keep the raw URL */ }
    return { id, name, controlUrl, displayBinding: "shared" };
  }
}

function relocateNode(node: ProvisionedNode, from: string, to: string): ProvisionedNode {
  const move = (value: string | undefined) => value && path.resolve(value).startsWith(from + path.sep) ? path.join(to, path.relative(from, value)) : value;
  return {
    ...node,
    caPath: move(node.caPath),
    certificatePath: move(node.certificatePath) ?? node.certificatePath,
    privateKeyPath: move(node.privateKeyPath) ?? node.privateKeyPath,
    publisherPublicKeyPath: move(node.publisherPublicKeyPath) ?? node.publisherPublicKeyPath,
  };
}

async function removeEmptyDirectory(directory: string): Promise<void> {
  try {
    if ((await readdir(directory)).length === 0) await rmdir(directory);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

export async function readJSON<T>(file: string): Promise<T | undefined> {
  try { return JSON.parse(await readFile(file, "utf8")) as T; }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw error;
  }
}

export async function atomicJSON(file: string, value: unknown): Promise<void> {
  const temporary = file + ".tmp";
  await writeFile(temporary, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });
  await rename(temporary, file);
}
