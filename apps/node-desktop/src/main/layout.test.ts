import { mkdir, mkdtemp, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, beforeEach, expect, test } from "vitest";
import { Layout } from "./layout";

let root: string;
beforeEach(async () => { root = await mkdtemp(path.join(os.tmpdir(), "cuelight-layout-")); });
afterEach(async () => { await rm(root, { recursive: true, force: true }); });

async function writeLegacy(): Promise<void> {
  const identity = path.join(root, "identity");
  await mkdir(identity, { recursive: true });
  await mkdir(path.join(root, "state", "cas"), { recursive: true });
  await mkdir(path.join(root, "logs"), { recursive: true });
  await writeFile(path.join(root, "state", "player.db"), "db");
  await writeFile(path.join(root, "logs", "player.log"), "log");
  await writeFile(path.join(identity, "node.json"), JSON.stringify({
    version: 1, nodeId: "node-1", nodeName: "MacBook", controlUrl: "https://desk.tail.example/api/v1", eventEpoch: "e", publisherKeyId: "p",
    caPath: path.join(identity, "controller-ca.pem"), certificatePath: path.join(identity, "node.pem"), privateKeyPath: path.join(identity, "node-key.pem"), publisherPublicKeyPath: path.join(identity, "publisher-public-key"),
  }));
  await writeFile(path.join(root, "settings.json"), JSON.stringify({ autoStart: true, fullScreen: false, presentationMode: "web" }));
}

test("legacy single-network layout moves into networks/default once, keeping preferences", async () => {
  await writeLegacy();
  const layout = new Layout(root);
  const settings = await layout.loadSettings();

  expect(settings).toEqual({ launchAtLogin: true, fullScreen: false, presentationMode: "web", networks: [{ id: "default", name: "desk.tail.example", controlUrl: "https://desk.tail.example", displayBinding: "shared", enabled: true }] });
  const node = JSON.parse(await readFile(path.join(root, "networks/default/identity/node.json"), "utf8"));
  expect(node.certificatePath).toBe(path.join(root, "networks/default/identity/node.pem"));
  expect(node.caPath).toBe(path.join(root, "networks/default/identity/controller-ca.pem"));
  expect(await readFile(path.join(root, "networks/default/state/player.db"), "utf8")).toBe("db");
  expect(await readFile(path.join(root, "networks/default/logs/player.log"), "utf8")).toBe("log");
  await expect(stat(path.join(root, "identity"))).rejects.toThrow();

  // A later legacy-looking directory (e.g. an old build ran again) is not migrated twice.
  await mkdir(path.join(root, "identity"));
  await writeFile(path.join(root, "identity", "node.json"), "{}");
  expect((await new Layout(root).loadSettings()).networks).toHaveLength(1);
});

test("multi-network settings with the old autoStart flag become per-network desired state", async () => {
  const network = (id: string, extra: Record<string, unknown> = {}) => ({ id, name: id, controlUrl: `https://${id}.example`, displayBinding: "shared", ...extra });
  await writeFile(path.join(root, "settings.json"), JSON.stringify({ autoStart: false, fullScreen: true, presentationMode: "automatic", networks: [network("home"), network("studio", { enabled: true })] }));
  const settings = await new Layout(root).loadSettings();
  expect(settings.launchAtLogin).toBe(false);
  // Without autoStart nothing started at launch before; an explicit enabled wins.
  expect(settings.networks.map(({ id, enabled }) => [id, enabled])).toEqual([["home", false], ["studio", true]]);
  expect(JSON.parse(await readFile(path.join(root, "settings.json"), "utf8"))).not.toHaveProperty("autoStart");

  await writeFile(path.join(root, "settings.json"), JSON.stringify({ autoStart: true, networks: [network("home")] }));
  const enabled = await new Layout(root).loadSettings();
  expect([enabled.launchAtLogin, enabled.networks[0]?.enabled]).toEqual([true, true]);
});

test("migration resumes after an interruption between moving identity and saving settings", async () => {
  await writeLegacy();
  // Simulate a crash right after identity/ moved: state/ and logs/ still at the root, node.json paths stale.
  await mkdir(path.join(root, "networks", "default"), { recursive: true });
  await rename(path.join(root, "identity"), path.join(root, "networks", "default", "identity"));

  const settings = await new Layout(root).loadSettings();
  expect(settings.networks.map((network) => network.id)).toEqual(["default"]);
  expect(await readFile(path.join(root, "networks/default/state/player.db"), "utf8")).toBe("db");
  const node = JSON.parse(await readFile(path.join(root, "networks/default/identity/node.json"), "utf8"));
  expect(node.privateKeyPath).toBe(path.join(root, "networks/default/identity/node-key.pem"));
});

test("unprovisioned legacy installs start with no networks and lose their empty directories", async () => {
  for (const name of ["identity", "state", "logs"]) await mkdir(path.join(root, name));
  const settings = await new Layout(root).loadSettings();
  expect(settings.networks).toEqual([]);
  await expect(stat(path.join(root, "identity"))).rejects.toThrow();
  await expect(stat(path.join(root, "networks", "default"))).rejects.toThrow();
});
