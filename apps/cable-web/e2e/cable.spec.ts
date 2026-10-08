import { expect, test, type Page } from "@playwright/test";
import { presentationFixture } from "../src/presentation";
function livePresentation() {
  const now = Date.now();
  const startsAt = new Date(now - 60_000).toISOString();
  const endsAt = new Date(now + 10 * 60_000).toISOString();
  return {
    ...presentationFixture,
    revision: { ...presentationFixture.revision, guideDensity: "half-hour" },
    blocks: [
      { id: "block-main", channelId: "channel-1", startsAt: new Date(now - 30 * 60_000).toISOString(), endsAt: new Date(now + 90 * 60_000).toISOString(), title: "Signal rotation", source: "playlist" as const },
      { id: "block-gallery", channelId: "channel-2", startsAt: new Date(now - 10 * 60_000).toISOString(), endsAt: new Date(now + 50 * 60_000).toISOString(), title: "Night block", source: "asset" as const },
    ],
    occurrences: presentationFixture.occurrences.map((occurrence, index) => ({ ...occurrence, blockId: index === 0 ? "block-main" : "block-gallery", startsAt, endsAt })),
  };
}


const guideURL = "/cable/?deployment=dep-1#token=guide-token";

async function interceptGuide(page: Page) {
  const authorizations: string[] = [];
  await page.route("**/api/v1/cable-guide/presentation", async (route) => {
    authorizations.push(route.request().headers().authorization ?? "");
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(livePresentation()) });
  });
  await page.route("**/api/v1/cable-guide/media/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "image/png",
      body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP4z8DwH4QZGBgYGMAAAAD//wMAHikE/QAAAABJRU5ErkJggg==", "base64"),
    });
  });
  return () => authorizations;
}

test("viewer sees the legacy splash, browses the guide, and opens a programme", async ({ page }) => {
  const authorizations = await interceptGuide(page);
  await page.goto(guideURL);
  await expect(page).toHaveURL(/\/cable\/\?deployment=dep-1$/);
  await expect(page.getByText("Chiba Cable Television", { exact: true })).toBeVisible();
  await expect(page.getByText("Chiba Cable Television", { exact: true })).toBeHidden({ timeout: 6_000 });
  await expect(page.locator(".header-title")).toHaveText("Signal / Sea");
  expect(authorizations()[0]).toBe("Bearer guide-token");
  await expect(page.locator(".program-card")).toHaveCount(2);
  await expect(page.locator(".time-slot")).toHaveCount(6);
  await page.getByRole("button", { name: /Night block/ }).dblclick();
  await expect(page.getByRole("img", { name: "Night archive" })).toBeVisible();
  await expect(page.locator(".player-toolbar, .player-title, .player-controls")).toHaveCount(0);
  await expect(page.locator(".player-viewport")).toHaveCSS("width", "1280px");
  await expect(page.locator(".player-viewport")).toHaveCSS("height", "720px");
});

test("viewer can tune channels with the legacy grid controls", async ({ page }) => {
  await interceptGuide(page);
  await page.goto(guideURL);
  await expect(page.locator(".splash-screen")).toBeHidden({ timeout: 6_000 });
  const gallery = page.getByRole("button", { name: /02 Gallery GALL/ });
  await gallery.click();
  await page.locator(".guide-shell").focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("img", { name: "Night archive" })).toBeVisible();
});

test("rapid remote route changes are processed once and in order", async ({ page }) => {
  const live = livePresentation();
  const presentation = {
    ...live,
    occurrences: live.occurrences.map((occurrence) => occurrence.channelId === "channel-1" ? { ...occurrence, media: { kind: "image" as const, reference: occurrence.media.reference, mime: "image/png" as const } } : occurrence),
    remote: { url: "https://control.example/remote/session#token=secret", showQr: false, expiresAt: new Date(Date.now() + 60_000).toISOString() },
  };
  let polls = 0;
  await page.route("**/api/v1/cable-guide/presentation", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(presentation) }));
  await page.route("**/api/v1/cable-guide/media/**", (route) => route.fulfill({ status: 200, contentType: "image/png", body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP4z8DwH4QZGBgYGMAAAAD//wMAHikE/QAAAABJRU5ErkJggg==", "base64") }));
  await page.route("**/api/v1/cable-guide/remote/events**", async (route) => {
    polls += 1;
    if (polls === 1) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [], next: 0 }) });
    if (polls === 2) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [
      { sequence: 1, type: "key", key: "ChannelDown" },
      { sequence: 2, type: "key", key: "Guide" },
      { sequence: 3, type: "key", key: "ChannelNumber:1" },
    ], next: 3 }) });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [], next: 3 }) });
  });
  await page.goto(guideURL);
  await expect(page.getByRole("img", { name: "Signal / Sea" })).toBeVisible({ timeout: 6_000 });
});

test("viewer can retry a failed guide load", async ({ page }) => {
  let attempts = 0;
  await page.route("**/api/v1/cable-guide/presentation", async (route) => {
    attempts += 1;
    if (attempts <= 2) {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "offline" }) });
      return;
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(livePresentation()) });
  });
  await page.goto(guideURL);
  await expect(page.getByRole("heading", { name: "Unable to load lineup" })).toBeVisible();
  await expect(page.locator(".splash-screen")).toBeHidden({ timeout: 6_000 });
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.locator(".header-title")).toHaveText("Signal / Sea");
});

test("a link without a guide token shows a clear access error", async ({ page }) => {
  await page.goto("/cable/?deployment=dep-1");
  await expect(page.getByRole("heading", { name: "Guide access unavailable" })).toBeVisible();
  await expect(page.getByText(/missing its access token/)).toBeVisible();
});

test("an expired guide token shows a clear access error", async ({ page }) => {
  await page.route("**/api/v1/cable-guide/presentation", (route) => route.fulfill({ status: 401 }));
  await page.goto(guideURL);
  await expect(page.getByRole("heading", { name: "Guide access unavailable" })).toBeVisible();
  await expect(page.getByText(/access has expired/)).toBeVisible();
});
