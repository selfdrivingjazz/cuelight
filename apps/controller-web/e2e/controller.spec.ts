import { readFileSync } from "node:fs";
import { expect, test, type Page, type Route } from "@playwright/test";

const pageInfo = { nextCursor: null, previousCursor: null, hasMore: false };
const node = { id: "11111111-1111-1111-1111-111111111111", name: "Lobby display", credentialState: "active", desiredGeneration: 4, observedGeneration: 4 };
const deployment = { id: "22222222-2222-2222-2222-222222222222", name: "Atrium Cable", timezone: "America/New_York", theme: "gallery", guideHorizonSeconds: 86_400, guideDensity: "hour", version: 3, createdBy: "33333333-3333-3333-3333-333333333333", updatedBy: "33333333-3333-3333-3333-333333333333", createdAt: "2026-08-27T10:00:00Z", updatedAt: "2026-08-27T12:00:00Z" };
const uploadedAsset = { id: "55555555-5555-5555-5555-555555555555", name: "poster.webp", artifact: { digest: "a".repeat(64), size: 68, mediaType: "image/png", state: "ready" } };
const videoAsset = { id: "66666666-6666-6666-6666-666666666666", name: "lobby-loop.mp4", artifact: { digest: "b".repeat(64), size: 697_200, mediaType: "video/mp4", state: "ready" } };
const createdMedia = {
  id: "77777777-7777-7777-7777-777777777777",
  assetId: uploadedAsset.id,
  title: uploadedAsset.name,
  description: "A bright welcome image",
  artist: "Studio team",
  tags: ["welcome"],
  playback: { fit: "fit", loop: true, muted: true, volume: 0, orientation: 0 },
  artifact: uploadedAsset.artifact,
  version: 1,
  createdBy: "operator-one",
  updatedBy: "operator-one",
  createdAt: "2026-09-02T10:00:00Z",
  updatedAt: "2026-09-02T10:00:00Z",
};
const videoMedia = {
  ...createdMedia,
  id: "88888888-8888-8888-8888-888888888888",
  assetId: videoAsset.id,
  title: "Lobby Loop",
  description: "Morning motion ident",
  artist: "Motion team",
  tags: ["lobby", "loop"],
  artifact: videoAsset.artifact,
};
const imageBytes = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64");
const videoBytes = readFileSync(new URL("../../../.dev-seed/Seed Violet Relay.mp4", import.meta.url));

interface ApiCapture {
  previewAuthorizations: string[];
  createdMediaBody?: Record<string, unknown>;
  patchHeaders?: Record<string, string>;
}

async function installControllerApi(page: Page, requireToken = false): Promise<ApiCapture> {
  const capture: ApiCapture = { previewAuthorizations: [] };
  await page.route("**/health", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ status: "ok", version: "browser-test" }) }));
  await page.route("**/api/v1/**", async (route: Route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1", "");
    const reply = (json: unknown, status = 200, headers: Record<string, string> = {}) => route.fulfill({ status, headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(json) });
    if (path === "/operator/me") {
      if (requireToken && request.headers().authorization !== "Bearer operator-token") return reply({ title: "Unauthorized", status: 401 }, 401);
      return reply({ id: "operator-one", scopes: ["node_read", "node_enroll", "media_read", "media_write", "cable_read", "cable_write"], capabilities: { node_read: true, node_enroll: true, media_read: true, media_write: true, cable_read: true, cable_write: true } });
    }
    if (path === "/health") return reply({ status: "ok", version: "browser-test" });
    if (path === "/nodes") return reply({ items: [node], page: pageInfo });
    if (path === `/nodes/${node.id}`) return reply(node, 200, { ETag: '"node:4:4"' });
    if (path === `/nodes/${node.id}/status`) return reply({ eventEpoch: "44444444-4444-4444-4444-444444444444", sequence: 4, desiredGenerationSeen: 4, observedGeneration: 4, presentationKind: "cable", conditions: [] });
    if (path === "/uploads" && request.method() === "GET") return reply({ items: [], page: pageInfo });
    if (path === "/uploads" && request.method() === "POST") return reply({ id: "upload-one", name: uploadedAsset.name, length: uploadedAsset.artifact.size, offset: 0, mediaType: uploadedAsset.artifact.mediaType, state: "open" }, 201);
    if (path === "/uploads/upload-one" && request.method() === "PATCH") return reply({ id: "upload-one", name: uploadedAsset.name, length: uploadedAsset.artifact.size, offset: uploadedAsset.artifact.size, mediaType: uploadedAsset.artifact.mediaType, state: "open" }, 200, { "Upload-Offset": String(uploadedAsset.artifact.size) });
    if (path === "/uploads/upload-one/complete" && request.method() === "POST") return reply(uploadedAsset);
    if (path === "/media" && request.method() === "GET") return reply({ items: [createdMedia, videoMedia], page: pageInfo });
    if (path === "/media" && request.method() === "POST") {
      capture.createdMediaBody = request.postDataJSON() as Record<string, unknown>;
      return reply(createdMedia, 201, { ETag: `"media:${createdMedia.id}:1"` });
    }
    if (path.endsWith("/content") && path.startsWith("/media/")) {
      capture.previewAuthorizations.push(request.headers().authorization ?? "");
      const isVideo = path.includes(videoMedia.id);
      return route.fulfill({ status: 200, headers: { "Content-Type": isVideo ? "video/mp4" : "image/png", ETag: isVideo ? '"video"' : '"image"' }, body: isVideo ? videoBytes : imageBytes });
    }
    if (path === `/media/${createdMedia.id}/usage`) return reply({ mediaId: createdMedia.id, releaseCount: 2, scheduleCount: 3 });
    if (path === `/media/${videoMedia.id}/usage`) return reply({ mediaId: videoMedia.id, releaseCount: 1, scheduleCount: 1 });
    if (path === `/media/${createdMedia.id}` && request.method() === "PATCH") {
      capture.patchHeaders = request.headers();
      const updated = { ...createdMedia, ...(request.postDataJSON() as object), version: 2, updatedAt: "2026-09-02T11:00:00Z" };
      return reply(updated, 200, { ETag: `"media:${createdMedia.id}:2"` });
    }
    if (path === `/media/${createdMedia.id}` && request.method() === "GET") return reply(createdMedia, 200, { ETag: `"media:${createdMedia.id}:1"` });
    if (path === `/media/${videoMedia.id}` && request.method() === "GET") return reply(videoMedia, 200, { ETag: `"media:${videoMedia.id}:1"` });
    if (path === `/assets/${uploadedAsset.id}` && request.method() === "GET") return reply(uploadedAsset);
    if (path === "/cable/deployments") return reply({ items: [deployment], page: pageInfo });
    if (path === `/cable/deployments/${deployment.id}`) return reply(deployment, 200, { ETag: '"v3"' });
    return reply({ items: [], page: pageInfo });
  });
  return capture;
}

async function authenticate(page: Page) {
  await page.goto("/");
  await page.getByRole("textbox", { name: "Operator token" }).fill("operator-token");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
}

test("operator authenticates and browses fleet detail", async ({ page }) => {
  await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("link", { name: "View nodes" }).click();
  await expect(page.getByRole("heading", { name: "Fleet", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Lobby display" }).click();
  await expect(page.getByRole("heading", { name: "Lobby display" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Assignment" })).toBeVisible();
});

test("operator upload creates authored media before opening its detail", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("link", { name: "Media", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Media Library" })).toBeVisible();
  await page.getByRole("button", { name: "Add media" }).click();

  const transfer = await page.evaluateHandle(() => {
    const data = new DataTransfer();
    data.items.add(new File([new Uint8Array(68)], "poster.webp", { type: "image/png" }));
    return data;
  });
  const dropzone = page.getByRole("group", { name: "Media upload" });
  await dropzone.dispatchEvent("dragenter", { dataTransfer: transfer });
  await expect(page.getByText("Drop to add this file")).toBeVisible();
  await dropzone.dispatchEvent("drop", { dataTransfer: transfer });
  await expect(page.getByText("poster.webp · 68 B")).toBeVisible();
  await page.getByRole("button", { name: "Upload file" }).click();

  await expect(page).toHaveURL(`/media/${createdMedia.id}`);
  await expect(page.getByRole("heading", { name: "poster.webp", exact: true })).toBeVisible();
  expect(capture.createdMediaBody).toEqual({
    assetId: uploadedAsset.id,
    title: "poster.webp",
    tags: [],
    playback: { fit: "fit", loop: true, muted: true, volume: 0, orientation: 0 },
  });

  await page.getByLabel("Title").fill("Lobby poster");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Changes saved." })).toBeVisible();
  expect(capture.patchHeaders?.["if-match"]).toBe(`"media:${createdMedia.id}:1"`);
  expect(capture.patchHeaders?.["idempotency-key"]).toBeTruthy();
});

test("media library serves authorized image and video previews responsively and revokes them", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await page.setViewportSize({ width: 390, height: 844 });
  await authenticate(page);
  await page.evaluate(() => {
    const nativeRevoke = URL.revokeObjectURL.bind(URL);
    (window as Window & { revokedPreviewUrls?: string[] }).revokedPreviewUrls = [];
    URL.revokeObjectURL = (url: string) => {
      (window as Window & { revokedPreviewUrls: string[] }).revokedPreviewUrls.push(url);
      nativeRevoke(url);
    };
  });
  await page.getByRole("button", { name: "Menu" }).click();
  await page.getByRole("link", { name: "Media", exact: true }).click();
  await expect(page.getByAltText("poster.webp preview")).toBeVisible();
  await expect(page.locator('video[aria-label="Lobby Loop preview"]')).toBeVisible();
  const mobileCards = page.locator("article");
  const firstMobile = await mobileCards.nth(0).boundingBox();
  const secondMobile = await mobileCards.nth(1).boundingBox();
  expect(secondMobile!.y).toBeGreaterThan(firstMobile!.y);

  await page.setViewportSize({ width: 1280, height: 900 });
  const firstDesktop = await mobileCards.nth(0).boundingBox();
  const secondDesktop = await mobileCards.nth(1).boundingBox();
  expect(secondDesktop!.x).toBeGreaterThan(firstDesktop!.x);
  expect(capture.previewAuthorizations).toEqual(expect.arrayContaining(["Bearer operator-token"]));

  await page.getByRole("link", { name: "Fleet", exact: true }).click();
  await expect.poll(() => page.evaluate(() => (window as Window & { revokedPreviewUrls?: string[] }).revokedPreviewUrls?.length ?? 0)).toBeGreaterThanOrEqual(2);
});

test("operator browses Cable deployment hierarchy", async ({ page }) => {
  await installControllerApi(page);
  await page.goto("/");
  await expect(page.getByRole("textbox", { name: "Operator token" })).toHaveCount(0);
  await page.getByRole("link", { name: "Deployments" }).click();
  await expect(page.getByRole("heading", { name: "Cable deployments" })).toBeVisible();
  await page.getByRole("link", { name: "Atrium Cable" }).click();
  await expect(page.getByRole("heading", { name: "Atrium Cable" })).toBeVisible();
  await expect(page.getByLabel("Deployment sections").getByRole("link", { name: "Channels" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Schedule" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Revisions" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Publish" })).toBeVisible();
});

test("phone remote preserves legacy controls and orders rapid input", async ({ page }) => {
  await installControllerApi(page);
  const events: Array<{ sequence: number; key: string }> = [];
  await page.route("**/api/v1/remote/sessions/session/events", async (route) => {
    events.push(route.request().postDataJSON() as { sequence: number; key: string });
    await route.fulfill({ status: 202, body: "" });
  });
  await page.goto("/remote/session#token=remote-token");
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "CH UP" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Guide" })).toBeVisible();
  await expect(page.getByRole("button", { name: "7", exact: true })).toBeVisible();
  await expect(page.getByLabel("Controller color theme")).toHaveCount(0);
  await page.evaluate(() => {
    for (const label of ["CH UP", "VOL UP", "Guide"]) {
      const button = [...document.querySelectorAll("button")].find((candidate) => candidate.textContent === label);
      button?.click();
    }
  });
  await expect.poll(() => events.length).toBe(3);
  expect(events.map((event) => event.key)).toEqual(["ChannelUp", "VolumeUp", "Guide"]);
  expect(events[0]!.sequence).toBeLessThan(events[1]!.sequence);
  expect(events[1]!.sequence).toBeLessThan(events[2]!.sequence);
});
