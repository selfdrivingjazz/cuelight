import { expect, test, type Page, type Route } from "@playwright/test";

const pageInfo = { nextCursor: null, previousCursor: null, hasMore: false };
const node = { id: "11111111-1111-1111-1111-111111111111", name: "Lobby display", credentialState: "active", desiredGeneration: 4, observedGeneration: 4 };
const deployment = { id: "22222222-2222-2222-2222-222222222222", name: "Atrium Cable", timezone: "America/New_York", theme: "gallery", guideHorizonSeconds: 86_400, guideDensity: "hour", version: 3, createdBy: "33333333-3333-3333-3333-333333333333", updatedBy: "33333333-3333-3333-3333-333333333333", createdAt: "2026-08-27T10:00:00Z", updatedAt: "2026-08-27T12:00:00Z" };
const uploadedAsset = { id: "55555555-5555-5555-5555-555555555555", name: "poster.webp", artifact: { digest: "a".repeat(64), size: 4, mediaType: "image/webp", state: "ready" } };

async function installControllerApi(page: Page, requireToken = false) {
  await page.route("**/health", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ status: "ok", version: "browser-test" }) }));
  await page.route("**/api/v1/**", async (route: Route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1", "");
    const reply = (json: unknown, status = 200, headers: Record<string, string> = {}) => route.fulfill({ status, headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(json) });
    if (path === "/operator/me") {
      if (requireToken && request.headers().authorization !== "Bearer operator-token") return reply({ title: "Unauthorized", status: 401 }, 401);
      return reply({ id: "operator-one", scopes: ["node_read", "node_enroll", "media_write", "cable_read", "cable_write"], capabilities: { node_read: true, node_enroll: true, media_write: true, cable_read: true, cable_write: true } });
    }
    if (path === "/health") return reply({ status: "ok", version: "browser-test" });
    if (path === "/nodes") return reply({ items: [node], page: pageInfo });
    if (path === `/nodes/${node.id}`) return reply(node, 200, { ETag: '"node:4:4"' });
    if (path === `/nodes/${node.id}/status`) return reply({ eventEpoch: "44444444-4444-4444-4444-444444444444", sequence: 4, desiredGenerationSeen: 4, observedGeneration: 4, presentationKind: "cable", conditions: [] });
    if (path === "/uploads" && request.method() === "GET") return reply({ items: [], page: pageInfo });
    if (path === "/uploads" && request.method() === "POST") return reply({ id: "upload-one", name: uploadedAsset.name, length: uploadedAsset.artifact.size, offset: 0, mediaType: uploadedAsset.artifact.mediaType, state: "open" }, 201);
    if (path === "/uploads/upload-one" && request.method() === "PATCH") return reply({ id: "upload-one", name: uploadedAsset.name, length: uploadedAsset.artifact.size, offset: uploadedAsset.artifact.size, mediaType: uploadedAsset.artifact.mediaType, state: "open" }, 200, { "Upload-Offset": String(uploadedAsset.artifact.size) });
    if (path === "/uploads/upload-one/complete" && request.method() === "POST") return reply(uploadedAsset);
    if (path === `/assets/${uploadedAsset.id}` && request.method() === "GET") return reply(uploadedAsset);
    if (path === "/cable/deployments") return reply({ items: [deployment], page: pageInfo });
    if (path === `/cable/deployments/${deployment.id}`) return reply(deployment, 200, { ETag: '"v3"' });
    return reply({ items: [], page: pageInfo });
  });
}

test("operator authenticates and browses fleet detail", async ({ page }) => {
  await installControllerApi(page, true);
  await page.goto("/");
  await page.getByRole("textbox", { name: "Operator token" }).fill("operator-token");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await page.getByRole("link", { name: "View nodes" }).click();
  await expect(page.getByRole("heading", { name: "Nodes", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Lobby display" }).click();
  await expect(page.getByRole("heading", { name: "Lobby display" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Assignment" })).toBeVisible();
});

test("operator drops media and opens the created asset", async ({ page }) => {
  await installControllerApi(page);
  await page.goto("/");
  await page.getByRole("link", { name: "Uploads" }).click();
  await expect(page.getByRole("heading", { name: "Uploads", exact: true })).toBeVisible();

  const transfer = await page.evaluateHandle(() => {
    const data = new DataTransfer();
    data.items.add(new File([new Uint8Array([82, 73, 70, 70])], "poster.webp", { type: "image/webp" }));
    return data;
  });
  const dropzone = page.getByRole("group", { name: "Media upload" });
  await dropzone.dispatchEvent("dragenter", { dataTransfer: transfer });
  await expect(page.getByText("Drop to add this file")).toBeVisible();
  await dropzone.dispatchEvent("drop", { dataTransfer: transfer });
  await expect(page.getByText("poster.webp · 4 B")).toBeVisible();
  await page.getByRole("button", { name: "Upload file" }).click();

  await expect(page.getByText("poster.webp is ready in your library.")).toBeVisible();
  await page.getByRole("link", { name: "View poster.webp" }).click();
  await expect(page.getByRole("heading", { name: "poster.webp", exact: true })).toBeVisible();
});

test("operator browses Cable deployment hierarchy", async ({ page }) => {
  await installControllerApi(page);
  await page.goto("/");
  await expect(page.getByRole("textbox", { name: "Operator token" })).toHaveCount(0);
  await page.getByRole("link", { name: "Deployments" }).click();
  await expect(page.getByRole("heading", { name: "Cable deployments" })).toBeVisible();
  await page.getByRole("link", { name: "Atrium Cable" }).click();
  await expect(page.getByRole("heading", { name: "Atrium Cable" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Channels" })).toBeVisible();
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
