import ffmpeg from "@ffmpeg-installer/ffmpeg";
import { mkdir, readFile, rm, stat } from "node:fs/promises";
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const output = path.join(root, ".dev-seed");
const apiBase = (process.env.CABLE_SEED_API ?? "http://127.0.0.1:4178/api/v1").replace(/\/$/, "");
const videos = [
  { key: "solar", name: "Seed Solar Drift.mp4", title: "Solar Drift", hue: "0" },
  { key: "lagoon", name: "Seed Lagoon Signal.mp4", title: "Lagoon Signal", hue: "95" },
  { key: "violet", name: "Seed Violet Relay.mp4", title: "Violet Relay", hue: "205" },
];

function run(command, args, cwd = root) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: "inherit" });
    child.once("error", reject);
    child.once("exit", (code) => code === 0 ? resolve() : reject(new Error(`${command} exited with ${code}`)));
  });
}

async function request(route, { method = "GET", body, headers = {}, raw = false } = {}) {
  const response = await fetch(`${apiBase}${route}`, {
    method,
    headers: { ...(raw ? {} : { "Content-Type": "application/json" }), ...headers },
    body: body === undefined ? undefined : raw ? body : JSON.stringify(body),
  });
  if (!response.ok) {
    const detail = await response.text();
    throw new Error(`${method} ${route} returned ${response.status}: ${detail}`);
  }
  if (response.status === 204) return undefined;
  return response.json();
}

async function generateVideos() {
  await mkdir(output, { recursive: true });
  for (const video of videos) {
    const destination = path.join(output, video.name);
    await run(ffmpeg.path, [
      "-y", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=8",
      "-vf", `hue=h=${video.hue}:s=1.15`, "-an", "-c:v", "libx264", "-preset", "veryfast",
      "-pix_fmt", "yuv420p", "-movflags", "+faststart", destination,
    ]);
  }
}

async function list(route) {
  return request(`${route}${route.includes("?") ? "&" : "?"}limit=100`);
}

async function assetByName(name) {
  const page = await request(`/assets?limit=100&search=${encodeURIComponent(name)}&sort=name&order=asc`);
  return page.items.find((asset) => asset.name === name);
}

async function upload(file, name, mediaType) {
  const existing = await assetByName(name);
  if (existing) return existing;
  const info = await stat(file);
  const upload = await request("/uploads", {
    method: "POST",
    headers: { "Idempotency-Key": `seed-upload-${name}` },
    body: { name, length: info.size, mediaType },
  });
  const content = await readFile(file);
  await request(`/uploads/${upload.id}`, {
    method: "PATCH",
    raw: true,
    headers: { "Content-Type": "application/offset+octet-stream", "Upload-Offset": "0" },
    body: content,
  });
  return request(`/uploads/${upload.id}/complete`, {
    method: "POST",
    headers: { "Idempotency-Key": `seed-complete-${name}` },
  });
}

async function publishMedia(asset, key) {
  return request("/releases/publish", {
    method: "POST",
    headers: { "Idempotency-Key": `seed-media-release-${key}` },
    body: { playback: { assetId: asset.id, fit: "fill", loop: true, muted: true, volume: 0, orientation: 0, allowedDeliveryModes: ["stream", "cache_then_play"] } },
  });
}

async function findByName(route, name) {
  const page = await list(route);
  return page.items.find((item) => item.name === name);
}

async function createNamed(route, name, body, key) {
  return await findByName(route, name) ?? request(route, { method: "POST", headers: { "Idempotency-Key": key }, body });
}
async function buildShell() {
  await run("pnpm", ["--filter", "@cable/cable-web", "build"]);
  const archive = path.join(output, "cable-web-shell.tar.gz");
  await rm(archive, { force: true });
  await run("tar", ["-czf", archive, "-C", path.join(root, "apps/cable-web/dist"), "index.html", "assets"]);
  const archiveDigest = createHash("sha256").update(await readFile(archive)).digest("hex").slice(0, 12);
  const asset = await upload(archive, `Seed Cable TV shell ${archiveDigest}.tar.gz`, "application/gzip");
  const builds = await list("/cable/shell-builds");
  const existing = builds.items.find((build) => build.digest === asset.artifact.digest);
  if (existing) return existing;
  return request("/cable/shell-builds", {
    method: "POST",
    headers: { "Idempotency-Key": "seed-cable-web-shell" },
    body: { digest: asset.artifact.digest, entrypoint: "index.html", artifactDigests: [asset.artifact.digest], bridgeProtocol: "1", versionMetadata: { version: "dev-seed", app: "@cable/cable-web" } },
  });
}

async function seed() {
  await generateVideos();
  const assets = [];
  const mediaReleases = [];
  for (const video of videos) {
    const asset = await upload(path.join(output, video.name), video.name, "video/mp4");
    assets.push(asset);
    mediaReleases.push(await publishMedia(asset, video.key));
  }
  const shellBuild = await buildShell();
  const deployment = await createNamed("/cable/deployments", "Cable Dev Lineup", {
    name: "Cable Dev Lineup", timezone: "UTC", theme: "nebula", guideHorizonSeconds: 86400,
    guideDensity: "comfortable", placardDefaults: {}, fallbackMetadata: {},
  }, "seed-cable-deployment");

  const playlistSpecs = [
    { name: "Signal Rotation", description: "Three deterministic generated videos.", order: [0, 1, 2] },
    { name: "Night Rotation", description: "The generated videos in reverse order.", order: [2, 1, 0] },
  ];
  const playlists = [];
  for (const spec of playlistSpecs) {
    playlists.push(await createNamed("/cable/playlists", spec.name, {
      name: spec.name,
      description: spec.description,
      items: spec.order.map((index, position) => ({ assetId: assets[index].id, position: position + 1, durationSeconds: 1200, title: videos[index].title, subtitle: "Generated Cable development programme" })),
    }, `seed-playlist-${spec.name.toLowerCase().replaceAll(" ", "-")}`));
  }

  const channelSpecs = [
    { number: 7, name: "Signal One", callsign: "SIG1", accent: "#5eead4", playlist: playlists[0] },
    { number: 12, name: "Night Gallery", callsign: "NGHT", accent: "#c084fc", playlist: playlists[1] },
    { number: 24, name: "Test Pattern", callsign: "TEST", accent: "#fbbf24", playlist: playlists[0] },
  ];
  const channelPage = await list(`/cable/deployments/${deployment.id}/channels`);
  const blockStart = new Date();
  blockStart.setUTCMinutes(0, 0, 0);
  blockStart.setUTCHours(blockStart.getUTCHours() - 1);
  const blockDate = blockStart.toISOString().slice(0, 10);
  const channels = [];
  const blocks = [];
  for (const spec of channelSpecs) {
    const channel = channelPage.items.find((item) => item.number === spec.number) ?? await request(`/cable/deployments/${deployment.id}/channels`, {
      method: "POST", headers: { "Idempotency-Key": `seed-cable-channel-${spec.number}` },
      body: { number: spec.number, name: spec.name, callsign: spec.callsign, description: "Generated development channel", accent: spec.accent, enabled: true, standby: false },
    });
    channels.push(channel);
    const existingBlocks = await list(`/cable/channels/${channel.id}/blocks`);
    const title = `${spec.name} ${blockDate}`;
    blocks.push(existingBlocks.items.find((item) => item.title === title) ?? await request(`/cable/channels/${channel.id}/blocks`, {
      method: "POST", headers: { "Idempotency-Key": `seed-block-${spec.number}-${blockDate}` },
      body: { playlistId: spec.playlist.id, startsAt: blockStart.toISOString(), title },
    }));
  }

  const window = { startsAt: blockStart.toISOString(), endsAt: new Date(blockStart.getTime() + 4 * 60 * 60 * 1000).toISOString() };
  const revision = await request(`/cable/deployments/${deployment.id}/publish`, {
    method: "POST", headers: { "Idempotency-Key": `seed-revision-${blockDate}-${shellBuild.digest.slice(0, 12)}`, "If-Match": `"v${deployment.version}"` },
    body: { shellBuildId: shellBuild.id, window },
  });
  const cableRelease = await request(`/cable/revisions/${revision.id}/release`, {
    method: "POST", headers: { "Idempotency-Key": `seed-cable-release-${revision.id.slice(0, 16)}` },
  });
  const profileName = `Cable Dev TV ${blockDate} ${shellBuild.digest.slice(0, 6)}`;
  const profile = await createNamed("/cable/profiles", profileName, {
    name: profileName, description: "Seeded TV guide backed by generated playlists and scheduled blocks.",
    deploymentId: deployment.id, shellBuildId: shellBuild.id, releaseId: cableRelease.id,
  }, `seed-profile-${blockDate}-${shellBuild.digest.slice(0, 12)}`);

  console.log(JSON.stringify({ assets, mediaReleases, playlists, blocks, channels, profile, cableRelease }, null, 2));
}

await seed();
