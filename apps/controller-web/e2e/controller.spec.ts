import { readFileSync } from "node:fs";
import { expect, test, type Page, type Route } from "@playwright/test";

const pageInfo = { nextCursor: null, previousCursor: null, hasMore: false };
const node = { id: "11111111-1111-1111-1111-111111111111", name: "Lobby display", credentialState: "active", desiredGeneration: 4, observedGeneration: 4 };
const uploadedAsset = { id: "55555555-5555-5555-5555-555555555555", name: "poster.webp", artifact: { digest: "a".repeat(64), size: 68, mediaType: "image/png", state: "ready" } };
const videoAsset = { id: "66666666-6666-6666-6666-666666666666", name: "lobby-loop.mp4", artifact: { digest: "b".repeat(64), size: 697_200, mediaType: "video/mp4", state: "ready" } };
const createdMedia = {
  id: "77777777-7777-7777-7777-777777777777",
  kind: "file",
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
  thumbnail: undefined as { digest: string; mediaType: string; width: number; height: number } | undefined,
};
const imagePosterThumbnail = { digest: "d".repeat(64), mediaType: "image/jpeg", width: 1, height: 1 };
const videoMedia = {
  ...createdMedia,
  id: "88888888-8888-8888-8888-888888888888",
  assetId: videoAsset.id,
  title: "Lobby Loop",
  description: "Morning motion ident",
  artist: "Motion team",
  tags: ["lobby", "loop"],
  artifact: videoAsset.artifact,
  thumbnail: { digest: "e".repeat(64), mediaType: "image/jpeg", width: 640, height: 360 },
};
const webMedia = {
  id: "abababab-abab-abab-abab-abababababab",
  kind: "web",
  url: "https://status.example.com/board",
  appAccess: false,
  title: "Status board",
  tags: [],
  version: 1,
  createdBy: "operator-one",
  updatedBy: "operator-one",
  createdAt: "2026-09-03T10:00:00Z",
  updatedAt: "2026-09-03T10:00:00Z",
  thumbnail: { digest: "f".repeat(64), mediaType: "image/png", width: 1920, height: 1080, capturedAt: "2026-09-03T10:01:00Z", source: "server" },
};
const appMedia = { ...webMedia, id: "cdcdcdcd-cdcd-cdcd-cdcd-cdcdcdcdcdcd", title: "Remote app", url: "https://apps.example.com/remote", appAccess: true, thumbnail: undefined };
const profile = { id: "efefefef-efef-efef-efef-efefefefefef", name: "Morning", description: "Weekday opening", version: 1, screens: [] as Record<string, unknown>[], createdBy: "operator-one", updatedBy: "operator-one", createdAt: "2026-09-04T08:00:00Z", updatedAt: "2026-09-04T08:00:00Z", active: false, lastActivatedAt: undefined as string | undefined };
const imageBytes = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64");
const videoBytes = readFileSync(new URL("../../../.dev-seed/Seed Violet Relay.mp4", import.meta.url));

interface ApiCapture {
  /** Paths of every original download; thumbnails must make these rare. */
  contentRequests: string[];
  thumbnailUploads: { authorization?: string; contentType?: string }[];
  claimBody?: Record<string, unknown>;
  createdMediaBody?: Record<string, unknown>;
  patchHeaders?: Record<string, string>;
  assignmentBodies: Record<string, unknown>[];
  profilePut?: { body: Record<string, unknown>; ifMatch?: string };
  activations: number;
}

async function installControllerApi(page: Page, requireToken = false): Promise<ApiCapture> {
  const capture: ApiCapture = { contentRequests: [], thumbnailUploads: [], assignmentBodies: [], activations: 0 };
  let savedProfile = { ...profile };
  // The fixture image stands in for a format the controller cannot decode (WebP), so the first operator to see it captures the poster.
  let imagePoster: typeof imagePosterThumbnail | undefined;
  const currentImage = () => ({ ...createdMedia, thumbnail: imagePoster });
  await page.route("**/health", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ status: "ok", version: "browser-test" }) }));
  await page.route("**/api/v1/**", async (route: Route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1", "");
    const reply = (json: unknown, status = 200, headers: Record<string, string> = {}) => route.fulfill({ status, headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(json) });
    if (path === "/operator/me") {
      const authorization = request.headers().authorization;
      // Without requireToken the mock behaves like control on a trusted network: token-less requests are a full operator.
      if (authorization ? authorization !== "Bearer operator-token" : requireToken) return reply({ title: "Unauthorized", status: 401, detail: "operator authentication required" }, 401);
      const identity = authorization ? { id: "operator-one", tokenId: "token-one", authMethod: "token" } : { id: "00000000-0000-4000-8000-7275737465ff", authMethod: "network", peerAddress: "100.101.102.103" };
      return reply({ ...identity, scopes: ["node_read", "node_enroll", "media_read", "media_write", "assignment_read", "assignment_write"], capabilities: { node_read: true, node_enroll: true, media_read: true, media_write: true, assignment_read: true, assignment_write: true } });
    }
    if (path === "/health") return reply({ status: "ok", version: "browser-test" });
    if (path === "/nodes") return reply({ items: [node], page: pageInfo });
    if (path === `/nodes/${node.id}`) return reply(node, 200, { ETag: '"node:4:4"' });
    if (path === `/nodes/${node.id}/status`) return reply({ eventEpoch: "44444444-4444-4444-4444-444444444444", sequence: 4, desiredGenerationSeen: 4, observedGeneration: 4, presentationKind: "web", conditions: [] });
    if (path === `/nodes/${node.id}/assignment` && request.method() === "PUT") {
      const body = request.postDataJSON() as Record<string, unknown>;
      capture.assignmentBodies.push(body);
      return reply({ id: "assignment-two", nodeId: node.id, mediaId: body.mediaId, releaseId: "99999999-9999-9999-9999-999999999998", releaseDigest: "d".repeat(64), deliveryMode: "stream", expectedGeneration: 4, desiredGeneration: 5, remoteInput: body.remoteInput, showQr: body.showQr });
    }
    if (path === `/nodes/${node.id}/assignment`) return reply({ id: "assignment-one", nodeId: node.id, mediaId: webMedia.id, releaseId: "99999999-9999-9999-9999-999999999999", releaseDigest: "c".repeat(64), deliveryMode: "stream", expectedGeneration: 3, desiredGeneration: 4, remoteInput: true, showQr: false });
    if (path === "/profiles" && request.method() === "GET") return reply({ items: [savedProfile] });
    if (path === "/profiles/active") return reply(savedProfile.active ? { profileId: savedProfile.id, profileName: savedProfile.name, activatedAt: savedProfile.lastActivatedAt, drifted: false, drift: [] } : { drifted: false, drift: [] });
    if (path === `/profiles/${profile.id}` && request.method() === "GET") return reply(savedProfile, 200, { ETag: `"version:${savedProfile.version}"` });
    if (path === `/profiles/${profile.id}` && request.method() === "PUT") {
      const body = request.postDataJSON() as Record<string, unknown> & { screens: Record<string, unknown>[] };
      capture.profilePut = { body, ifMatch: request.headers()["if-match"] };
      savedProfile = { ...savedProfile, ...body, version: savedProfile.version + 1 };
      return reply(savedProfile, 200, { ETag: `"version:${savedProfile.version}"` });
    }
    if (path === `/profiles/${profile.id}/activate` && request.method() === "POST") {
      capture.activations += 1;
      savedProfile = { ...savedProfile, active: true, lastActivatedAt: "2026-09-04T09:00:00Z" };
      return reply({ id: "activation-one", profileId: profile.id, activatedAt: "2026-09-04T09:00:00Z", activatedBy: "operator-one", results: savedProfile.screens.map((screen) => ({ nodeId: screen.nodeId, assignmentId: "assignment-three" })) }, 201);
    }
    if (path === `/nodes/${node.id}/remote-link` && request.method() === "POST") return reply({ sessionId: "session", remoteUrl: "http://127.0.0.1:4173/remote/session#token=remote-token", expiresAt: "2026-10-04T12:00:00Z" }, 201);
    if (path === "/browser-nodes/claims" && request.method() === "POST") {
      capture.claimBody = request.postDataJSON() as Record<string, unknown>;
      if (capture.claimBody.code !== "K7QM4XPD") return reply({ title: "Not Found", status: 404 }, 404);
      return reply({ id: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", name: capture.claimBody.name, nodeClass: "browser", credentialState: "active", createdAt: "2026-10-03T12:00:00Z" }, 201);
    }
    if (path === "/uploads" && request.method() === "GET") return reply({ items: [], page: pageInfo });
    if (path === "/uploads" && request.method() === "POST") return reply({ id: "upload-one", name: uploadedAsset.name, length: uploadedAsset.artifact.size, offset: 0, mediaType: uploadedAsset.artifact.mediaType, state: "open" }, 201);
    if (path === "/uploads/upload-one" && request.method() === "PATCH") return reply({ id: "upload-one", name: uploadedAsset.name, length: uploadedAsset.artifact.size, offset: uploadedAsset.artifact.size, mediaType: uploadedAsset.artifact.mediaType, state: "open" }, 200, { "Upload-Offset": String(uploadedAsset.artifact.size) });
    if (path === "/uploads/upload-one/complete" && request.method() === "POST") return reply(uploadedAsset);
    if (path === "/media" && request.method() === "GET") {
      const kind = new URL(request.url()).searchParams.get("kind");
      const all = [currentImage(), videoMedia, webMedia, appMedia];
      return reply({ items: kind ? all.filter((item) => item.kind === kind) : all, page: pageInfo });
    }
    if (path === "/media" && request.method() === "POST") {
      capture.createdMediaBody = request.postDataJSON() as Record<string, unknown>;
      if (capture.createdMediaBody.kind === "web") return reply({ ...webMedia, url: capture.createdMediaBody.url, title: capture.createdMediaBody.title ?? webMedia.title }, 201, { ETag: `"media:${webMedia.id}:1"` });
      return reply(createdMedia, 201, { ETag: `"media:${createdMedia.id}:1"` });
    }
    if (path.endsWith("/content") && path.startsWith("/media/")) {
      capture.contentRequests.push(path);
      const isVideo = path.includes(videoMedia.id);
      return route.fulfill({ status: 200, headers: { "Content-Type": isVideo ? "video/mp4" : "image/png", ETag: isVideo ? '"video"' : '"image"' }, body: isVideo ? videoBytes : imageBytes });
    }
    if (path === `/media/${createdMedia.id}/thumbnail` && request.method() === "PUT") {
      capture.thumbnailUploads.push({ authorization: request.headers().authorization, contentType: request.headers()["content-type"] });
      imagePoster = imagePosterThumbnail;
      return reply(currentImage());
    }
    if (path.endsWith("/thumbnail") && path.startsWith("/media/")) {
      return route.fulfill({ status: 200, headers: { "Content-Type": "image/png", "Cache-Control": "private, max-age=31536000, immutable" }, body: imageBytes });
    }
    if (path === `/media/${createdMedia.id}/usage`) return reply({ mediaId: createdMedia.id, releaseCount: 2, scheduleCount: 3 });
    if (path === `/media/${videoMedia.id}/usage`) return reply({ mediaId: videoMedia.id, releaseCount: 1, scheduleCount: 1 });
    if (path === `/media/${createdMedia.id}` && request.method() === "PATCH") {
      capture.patchHeaders = request.headers();
      const updated = { ...createdMedia, ...(request.postDataJSON() as object), version: 2, updatedAt: "2026-09-02T11:00:00Z" };
      return reply(updated, 200, { ETag: `"media:${createdMedia.id}:2"` });
    }
    if (path === `/media/${createdMedia.id}` && request.method() === "GET") return reply(currentImage(), 200, { ETag: `"media:${createdMedia.id}:1"` });
    if (path === `/media/${videoMedia.id}` && request.method() === "GET") return reply(videoMedia, 200, { ETag: `"media:${videoMedia.id}:1"` });
    if (path === `/media/${webMedia.id}` && request.method() === "GET") return reply(webMedia, 200, { ETag: `"media:${webMedia.id}:1"` });
    if (path === `/media/${appMedia.id}` && request.method() === "GET") return reply(appMedia, 200, { ETag: `"media:${appMedia.id}:1"` });
    if (path === `/assets/${uploadedAsset.id}` && request.method() === "GET") return reply(uploadedAsset);
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

test("a trusted network skips the token screen and still allows a token identity", async ({ page }) => {
  await installControllerApi(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Operator token" })).toHaveCount(0);
  await page.goto("/settings");
  await expect(page.getByText("Trusted network (100.101.102.103)")).toBeVisible();
  await page.getByRole("textbox", { name: "Operator token" }).fill("wrong-token");
  await page.getByRole("button", { name: "Use token" }).click();
  await expect(page.getByText("operator authentication required")).toBeVisible();
  await expect(page.getByText("Trusted network (100.101.102.103)")).toBeVisible();
  await page.getByRole("textbox", { name: "Operator token" }).fill("operator-token");
  await page.getByRole("button", { name: "Use token" }).click();
  await expect(page.getByText("token-one")).toBeVisible();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByText("Trusted network (100.101.102.103)")).toBeVisible();
});

test("operator authenticates and drills from the screen list into a screen's sections", async ({ page }) => {
  await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("link", { name: "Screens" }).click();
  await expect(page).toHaveURL("/screens");
  await expect(page.getByRole("heading", { name: "Screens", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Lobby display" }).click();
  await expect(page).toHaveURL(`/screens/${node.id}`);
  await expect(page.getByRole("heading", { name: "Lobby display" })).toBeVisible();
  await page.getByLabel("Screen sections").getByRole("link", { name: "Assignment" }).click();
  await expect(page).toHaveURL(`/screens/${node.id}/assignment`);
  const breadcrumb = page.getByRole("navigation", { name: "Breadcrumb" });
  await expect(breadcrumb.getByRole("link", { name: "Lobby display" })).toBeVisible();
  await breadcrumb.getByRole("link", { name: "Screens" }).click();
  await expect(page).toHaveURL("/screens");
});

test("screen remote tab mints a phone link with a scannable QR", async ({ page }) => {
  await installControllerApi(page);
  await page.goto(`/screens/${node.id}/remote`);
  await page.getByRole("button", { name: "Create phone link" }).click();
  await expect(page.getByLabel("Phone remote link")).toHaveValue("http://127.0.0.1:4173/remote/session#token=remote-token");
  await expect(page.getByAltText("QR code for the Lobby display phone remote")).toHaveAttribute("src", /^data:image\/png/);
});

test("playlist editor adds media via the picker, drag-reorders, and persists the order", async ({ page }) => {
  await installControllerApi(page);
  const playlistId = "cccccccc-cccc-cccc-cccc-cccccccccccc";
  const stamp = { createdBy: "operator-one", createdAt: "2026-09-01T08:00:00Z", updatedAt: "2026-09-01T09:00:00Z" };
  let playlist = { id: playlistId, name: "Lobby loop", description: "", version: 1, ...stamp, items: [{ id: "item-1", playlistId, mediaId: createdMedia.id, assetId: createdMedia.assetId, position: 1, durationSeconds: 30, title: createdMedia.title }] };
  const patches: Array<{ items: Array<{ mediaId?: string; assetId?: string; position: number; durationSeconds: number }> }> = [];
  await page.route(`**/api/v1/playlists/${playlistId}`, async (route) => {
    const request = route.request();
    if (request.method() === "PATCH") {
      const body = request.postDataJSON() as (typeof patches)[number];
      patches.push(body);
      playlist = { ...playlist, version: playlist.version + 1, items: body.items.map((item, index) => ({ ...item, id: playlist.items.find((existing) => existing.mediaId === item.mediaId)?.id ?? `item-${index + 10}`, playlistId, mediaId: item.mediaId!, assetId: item.mediaId === videoMedia.id ? videoMedia.assetId : createdMedia.assetId, title: item.mediaId === videoMedia.id ? videoMedia.title : createdMedia.title })) };
    }
    await route.fulfill({ status: 200, headers: { "Content-Type": "application/json", ETag: `"v${playlist.version}"` }, body: JSON.stringify(playlist) });
  });
  await page.goto(`/library/playlists/${playlistId}`);
  await expect(page.getByRole("heading", { name: "Lobby loop" })).toBeVisible();

  await page.getByRole("button", { name: "Add media" }).click();
  const picker = page.getByRole("dialog", { name: "Add media to playlist" });
  await expect(picker.getByLabel("Search media")).toBeFocused();
  await expect(picker.getByRole("button", { name: /poster\.webp/ })).toHaveCount(0);
  await picker.getByRole("button", { name: "Lobby Loop, Video" }).click();
  await picker.getByLabel("Duration for added media (seconds)").fill("45");
  await picker.getByRole("button", { name: "Add 1 item" }).click();
  await expect(picker).toBeHidden();
  await expect(page.getByText("Media added to playlist.")).toBeVisible();
  // Every item is re-sent with exactly one source: its media id.
  expect(patches[0]!.items.map((item) => [item.mediaId, item.assetId, item.durationSeconds])).toEqual([[createdMedia.id, undefined, 30], [videoMedia.id, undefined, 45]]);

  const handle = page.getByRole("button", { name: "Reorder Lobby Loop, position 2 of 2" });
  const target = page.getByRole("button", { name: `Reorder ${createdMedia.title}, position 1 of 2` });
  const from = (await handle.boundingBox())!;
  const to = (await target.boundingBox())!;
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y - 20, { steps: 8 });
  await expect(page.getByTestId("drop-indicator")).toBeVisible();
  await page.mouse.up();
  await expect(page.getByRole("region", { name: "Notifications" }).getByRole("status").filter({ hasText: "Playlist order saved." })).toBeVisible();
  expect(patches[1]!.items.map((item) => item.mediaId)).toEqual([videoMedia.id, createdMedia.id]);

  // Keyboard reorder: lift the second row, move it up, drop.
  await page.getByRole("button", { name: `Reorder ${createdMedia.title}, position 2 of 2` }).focus();
  await page.keyboard.press("Space");
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("Space");
  await expect.poll(() => patches.length).toBe(3);
  expect(patches[2]!.items.map((item) => item.mediaId)).toEqual([createdMedia.id, videoMedia.id]);

  await page.reload();
  const rows = page.getByRole("list", { name: "Playlist order" }).getByRole("heading", { level: 3 });
  await expect(rows).toHaveText([createdMedia.title, "Lobby Loop"]);
});

test("playlist editor adds a web page by media id", async ({ page }) => {
  await installControllerApi(page);
  const playlistId = "dddddddd-dddd-dddd-dddd-dddddddddddd";
  const stamp = { createdBy: "operator-one", createdAt: "2026-09-01T08:00:00Z", updatedAt: "2026-09-01T09:00:00Z" };
  let playlist = { id: playlistId, name: "Wall loop", description: "", version: 1, ...stamp, items: [{ id: "item-1", playlistId, mediaId: createdMedia.id, assetId: createdMedia.assetId, position: 1, durationSeconds: 30, title: createdMedia.title }] as Record<string, unknown>[] };
  const patches: Array<{ items: Record<string, unknown>[] }> = [];
  await page.route(`**/api/v1/playlists/${playlistId}`, async (route) => {
    const request = route.request();
    if (request.method() === "PATCH") {
      const body = request.postDataJSON() as (typeof patches)[number];
      patches.push(body);
      playlist = { ...playlist, version: playlist.version + 1, items: body.items.map((item, index) => ({ ...item, id: `item-${index + 1}`, playlistId })) };
    }
    await route.fulfill({ status: 200, headers: { "Content-Type": "application/json", ETag: `"v${playlist.version}"` }, body: JSON.stringify(playlist) });
  });
  await page.goto(`/library/playlists/${playlistId}`);
  await expect(page.getByRole("heading", { name: "Wall loop" })).toBeVisible();
  await expect(page.getByRole("note")).toHaveCount(0);

  await page.getByRole("button", { name: "Add media" }).click();
  const picker = page.getByRole("dialog", { name: "Add media to playlist" });
  await picker.getByRole("radio", { name: "Web" }).click();
  await expect(picker.getByRole("button", { name: "Status board, Web page" })).toBeVisible();
  await picker.getByRole("button", { name: "Status board, Web page" }).click();
  await picker.getByLabel("Duration for added media (seconds)").fill("20");
  await picker.getByRole("button", { name: "Add 1 item" }).click();
  await expect(picker).toBeHidden();
  await expect.poll(() => patches.length).toBe(1);
  expect(patches[0]!.items).toEqual([
    { mediaId: createdMedia.id, position: 1, durationSeconds: 30, title: createdMedia.title },
    { mediaId: webMedia.id, position: 2, durationSeconds: 20, title: webMedia.title },
  ]);
  await expect(page.getByRole("note")).toContainText("Appliance players cannot show a playlist that contains them");
  await expect(page.getByRole("list", { name: "Playlist order" }).getByRole("heading", { level: 3 })).toHaveText([createdMedia.title, webMedia.title]);
});

test("pair page claims a browser screen from a prefilled code", async ({ page }) => {
  const capture = await installControllerApi(page);
  await page.goto("/pair?code=k7qm-4xpd");
  await expect(page.getByLabel("Pairing code")).toHaveValue("k7qm-4xpd");
  await page.getByLabel("Screen name").fill("Lobby TV");
  await page.getByRole("button", { name: "Pair screen" }).click();
  await expect(page.getByText("Paired “Lobby TV”.")).toBeVisible();
  expect(capture.claimBody).toEqual({ code: "K7QM4XPD", name: "Lobby TV" });
  await expect(page.getByRole("link", { name: "Choose what it shows" })).toHaveAttribute("href", "/screens/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/assignment");

  await page.getByRole("button", { name: "Pair another" }).click();
  await page.getByLabel("Pairing code").fill("WRONG-CODE");
  await page.getByLabel("Screen name").fill("Bar TV");
  await page.getByRole("button", { name: "Pair screen" }).click();
  await expect(page.getByRole("alert")).toContainText("Code not found");
});

test("operator upload creates authored media before opening its detail", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("link", { name: "Media", exact: true }).click();
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
  await page.getByRole("button", { name: "Upload media" }).click();

  await expect(page).toHaveURL(`/library/media/${createdMedia.id}`);
  await expect(page.getByRole("heading", { name: "poster.webp", exact: true })).toBeVisible();
  expect(capture.createdMediaBody).toEqual({
    kind: "file",
    appAccess: false,
    assetId: uploadedAsset.id,
    title: "poster.webp",
    tags: [],
    playback: { fit: "fit", loop: true, muted: true, volume: 0, orientation: 0 },
  });

  await page.getByLabel("Title").fill("Lobby poster");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("region", { name: "Notifications" }).getByRole("status").filter({ hasText: "Changes saved." })).toBeVisible();
  expect(capture.patchHeaders?.["if-match"]).toBe(`"media:${createdMedia.id}:1"`);
  expect(capture.patchHeaders?.["idempotency-key"]).toBeTruthy();
});

test("media library renders persisted thumbnails and captures a missing poster once", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await page.setViewportSize({ width: 390, height: 844 });
  await authenticate(page);
  await page.getByRole("button", { name: "Menu" }).click();
  await page.getByRole("link", { name: "Media", exact: true }).click();
  // The video already has a persisted poster: its card never touches the original.
  await expect(page.getByAltText("Lobby Loop thumbnail")).toBeVisible();
  // The image has none yet: one capture uploads a JPEG, then the card shows it.
  await expect(page.getByAltText("poster.webp thumbnail")).toBeVisible();
  expect(capture.thumbnailUploads).toEqual([{ authorization: "Bearer operator-token", contentType: "image/jpeg" }]);
  // Development StrictMode may abort and repeat the capture fetch; only the captured item's original is ever read.
  expect(new Set(capture.contentRequests)).toEqual(new Set([`/media/${createdMedia.id}/content`]));
  const mobileCards = page.locator("article");
  const firstMobile = await mobileCards.nth(0).boundingBox();
  const secondMobile = await mobileCards.nth(1).boundingBox();
  expect(secondMobile!.y).toBeGreaterThan(firstMobile!.y);

  await page.setViewportSize({ width: 1280, height: 900 });
  const firstDesktop = await mobileCards.nth(0).boundingBox();
  const secondDesktop = await mobileCards.nth(1).boundingBox();
  expect(secondDesktop!.x).toBeGreaterThan(firstDesktop!.x);

  // With posters persisted, a reload and the video detail page download no originals until play.
  capture.contentRequests.length = 0;
  await page.reload();
  await expect(page.getByAltText("poster.webp thumbnail")).toBeVisible();
  await page.getByRole("link", { name: "Lobby Loop" }).click();
  await expect(page.getByAltText("Lobby Loop preview")).toBeVisible();
  expect(capture.contentRequests).toEqual([]);
  expect(capture.thumbnailUploads).toHaveLength(1);
  await page.getByRole("button", { name: "Play video" }).click();
  await expect(page.locator('video[aria-label="Lobby Loop player"]')).toBeVisible();
  expect(capture.contentRequests).toEqual([`/media/${videoMedia.id}/content`]);
});

test("media library sort is reflected in the URL and sent to the API", async ({ page }) => {
  await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("link", { name: "Media", exact: true }).click();
  const listRequest = page.waitForRequest((request) => request.url().includes("/api/v1/media?") && request.url().includes("sort=size"));
  await page.getByLabel("Sort media").selectOption({ label: "Largest first" });
  await expect(page).toHaveURL(/sort=size/);
  const url = new URL((await listRequest).url());
  expect(url.searchParams.get("sort")).toBe("size");
  expect(url.searchParams.get("order")).toBe("desc");
  await page.getByLabel("Sort media").selectOption({ label: "Name A–Z" });
  await expect(page).toHaveURL(/sort=title&order=asc|order=asc.*sort=title/);
});

test("operator adds a web page as media and assigns it to a screen by media id", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("link", { name: "Media", exact: true }).click();
  // Web pages and Cuelight apps live in the same grid as files.
  await expect(page.getByRole("link", { name: "Status board" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Remote app" })).toBeVisible();
  await page.getByRole("button", { name: "Add web page" }).click();
  await page.getByLabel("Page URL").fill("https://status.example.com/board");
  await page.getByRole("checkbox", { name: /Cuelight app/ }).check();
  await page.getByRole("button", { name: "Add web page" }).last().click();
  await expect(page).toHaveURL(`/library/media/${webMedia.id}`);
  expect(capture.createdMediaBody).toEqual({ kind: "web", url: "https://status.example.com/board", appAccess: true });
  await expect(page.getByRole("heading", { name: "Status board", exact: true })).toBeVisible();
  await page.getByLabel("Screen").selectOption({ label: "Lobby display" });
  await page.getByRole("button", { name: "Assign to screen" }).click();
  await expect(page.getByRole("region", { name: "Notifications" }).getByRole("status").filter({ hasText: "Assignment updated." })).toBeVisible();
  expect(capture.assignmentBodies).toEqual([{ mediaId: webMedia.id, expectedGeneration: 4, remoteInput: false, showQr: false }]);
});

test("assignment composer picks any media kind from one picker", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await authenticate(page);
  await page.goto(`/screens/${node.id}/assignment`);
  await expect(page.getByLabel("Content type")).toHaveCount(0);
  await page.getByRole("button", { name: "Choose content" }).click();
  const picker = page.getByRole("dialog", { name: "Choose content" });
  await picker.getByRole("radio", { name: "Web" }).click();
  await picker.getByRole("button", { name: "Remote app, Cuelight app" }).click();
  // Web content always streams, so the delivery choice disappears.
  await expect(page.getByLabel("Delivery")).toHaveCount(0);
  await page.getByRole("button", { name: "Assign to screen" }).click();
  await expect.poll(() => capture.assignmentBodies.length).toBe(1);
  expect(capture.assignmentBodies[0]).toMatchObject({ mediaId: appMedia.id });
  expect(capture.assignmentBodies[0]).not.toHaveProperty("releaseId");
});

test("profile editor saves screen assignments and activation reports per-screen results", async ({ page }) => {
  const capture = await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("link", { name: "Profiles" }).click();
  await expect(page).toHaveURL("/profiles");
  await page.getByRole("link", { name: "Morning" }).click();
  await expect(page.getByRole("heading", { name: "Morning" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Activate" })).toBeDisabled();
  await page.getByRole("button", { name: "Add screen" }).click();
  await page.getByRole("list", { name: "Profile screens" }).getByRole("combobox").first().selectOption({ label: "Lobby display" });
  await page.getByRole("button", { name: "Choose content" }).click();
  await page.getByRole("dialog", { name: "Choose content" }).getByRole("button", { name: "Status board, Web page" }).click();
  await page.getByLabel("Allow remote control").check();
  await page.getByRole("button", { name: "Save profile" }).click();
  await expect(page.getByRole("region", { name: "Notifications" }).getByRole("status").filter({ hasText: "Profile saved." })).toBeVisible();
  expect(capture.profilePut).toEqual({ ifMatch: '"version:1"', body: { name: "Morning", description: "Weekday opening", screens: [{ nodeId: node.id, mediaId: webMedia.id, remoteInput: true, showQr: false }] } });

  await page.getByRole("button", { name: "Activate" }).click();
  const results = page.getByRole("region", { name: "Activation results" });
  await expect(results.getByText("Assigned")).toBeVisible();
  await expect(results.getByRole("link", { name: "Lobby display" })).toBeVisible();
  expect(capture.activations).toBe(1);
  await page.getByRole("navigation", { name: "Breadcrumb" }).getByRole("link", { name: "Profiles" }).click();
  await expect(page.getByRole("row", { name: /Morning/ }).getByText("Active")).toBeVisible();
});

test("releases are a read-only audit", async ({ page }) => {
  await installControllerApi(page, true);
  await authenticate(page);
  await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("link", { name: "Releases" }).click();
  await expect(page.getByRole("heading", { name: "Releases", level: 1 })).toBeVisible();
  await expect(page.getByRole("button", { name: /assign|publish/i })).toHaveCount(0);
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

test("phone trackpad taps click at the virtual cursor and keyboard posts text", async ({ page }) => {
  await installControllerApi(page);
  const events: Array<Record<string, unknown>> = [];
  await page.route("**/api/v1/remote/sessions/session/events", async (route) => {
    events.push(route.request().postDataJSON() as Record<string, unknown>);
    await route.fulfill({ status: 202, body: "" });
  });
  await page.goto("/remote/session#token=remote-token");
  await page.getByRole("tab", { name: "Trackpad" }).click();
  await page.getByRole("application", { name: "Trackpad" }).click();
  await expect.poll(() => events.length).toBe(1);
  expect(events[0]).toMatchObject({ type: "pointer", state: "click", x: 0.5, y: 0.5 });
  await page.getByRole("button", { name: "Keyboard" }).click();
  await expect(page.getByLabel("Type on screen")).toBeFocused();
  await page.keyboard.type("hi");
  await page.keyboard.press("Enter");
  await expect.poll(() => events.length).toBe(4);
  expect(events.slice(1)).toMatchObject([{ type: "text", text: "h" }, { type: "text", text: "i" }, { type: "key", key: "Enter" }]);
  const sequences = events.map((event) => event.sequence as number);
  expect(sequences).toEqual([...sequences].sort((a, b) => a - b));
});
