// Shared by set-version.mjs and check-version.mjs. VERSION at the repo root is
// the single source of the release version; every workspace package.json
// carries a copy (Electron reads its app version from package.json). Go builds
// stamp it via -ldflags, Vite builds via `define`, and the Android build reads
// VERSION directly.
import { readdirSync, existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
export const versionFile = resolve(root, "VERSION");
export const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

export function readVersion() {
  return readFileSync(versionFile, "utf8").trim();
}

/** Workspace package.json files (pnpm-workspace.yaml: apps/*, packages/*). */
export function packageFiles() {
  const files = [];
  for (const group of ["apps", "packages"]) {
    for (const entry of readdirSync(resolve(root, group), { withFileTypes: true })) {
      const file = resolve(root, group, entry.name, "package.json");
      if (entry.isDirectory() && existsSync(file)) files.push(file);
    }
  }
  return files;
}
