import ffmpeg from "@ffmpeg-installer/ffmpeg";
import { mkdir, readFile, stat } from "node:fs/promises";
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

async function seed() {
  await generateVideos();
  const assets = [];
  const mediaReleases = [];
  for (const video of videos) {
    const asset = await upload(path.join(output, video.name), video.name, "video/mp4");
    assets.push(asset);
    mediaReleases.push(await publishMedia(asset, video.key));
  }
  const playlistSpecs = [
    { name: "Signal Rotation", description: "Three deterministic generated videos.", order: [0, 1, 2] },
    { name: "Night Rotation", description: "The generated videos in reverse order.", order: [2, 1, 0] },
  ];
  const playlists = [];
  for (const spec of playlistSpecs) {
    playlists.push(await createNamed("/playlists", spec.name, {
      name: spec.name,
      description: spec.description,
      items: spec.order.map((index, position) => ({ assetId: assets[index].id, position: position + 1, durationSeconds: 1200, title: videos[index].title, subtitle: "Generated development video" })),
    }, `seed-playlist-${spec.name.toLowerCase().replaceAll(" ", "-")}`));
  }

  console.log(JSON.stringify({ assets, mediaReleases, playlists }, null, 2));
}

await seed();
