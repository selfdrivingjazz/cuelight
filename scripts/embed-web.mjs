#!/usr/bin/env node
// Copies the built browser apps into internal/control/webui/assets/ so that
// `go build ./cmd/cuelight-control` embeds them. Each app's embedded copy
// mirrors its current dist/: a missing dist removes the stale copy.
//
//   node scripts/embed-web.mjs [--require]
//
// --require fails unless both apps are built (release builds).
import { cpSync, existsSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const assets = resolve(root, "internal/control/webui/assets");
const apps = [
  { name: "controller-web", dist: resolve(root, "apps/controller-web/dist") },
  { name: "website-node", dist: resolve(root, "apps/website-node/dist") },
];
const requireAll = process.argv.includes("--require");

for (const app of apps) {
  const target = resolve(assets, app.name);
  rmSync(target, { recursive: true, force: true });
  if (!existsSync(resolve(app.dist, "index.html"))) {
    if (requireAll) {
      console.error(`embed-web: ${app.dist} is not built (run pnpm --filter @cuelight/${app.name} build)`);
      process.exit(1);
    }
    console.log(`embed-web: ${app.name} not built; control will serve a 'web assets not built' page for it`);
    continue;
  }
  cpSync(app.dist, target, { recursive: true });
  console.log(`embed-web: ${app.name} -> ${target}`);
}
