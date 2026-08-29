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


async function interceptBridge(page: Page) {
  let readyCalls = 0;
  await page.route("**/bridge/v1/presentation", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(livePresentation()) });
  });
  await page.route("**/bridge/v1/ready", async (route) => {
    readyCalls += 1;
    await route.fulfill({ status: 204 });
  });
  await page.route("**/bridge/v1/media/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "image/png",
      body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP4z8DwH4QZGBgYGMAAAAD//wMAHikE/QAAAABJRU5ErkJggg==", "base64"),
    });
  });
  return () => readyCalls;
}

test("viewer sees the legacy splash, browses the guide, and opens a programme", async ({ page }) => {
  const readinessCount = await interceptBridge(page);
  await page.goto("/");
  await expect(page.getByText("Chiba Cable Television", { exact: true })).toBeVisible();
  await expect(page.getByText("Chiba Cable Television", { exact: true })).toBeHidden({ timeout: 6_000 });
  await expect(page.locator(".header-title")).toHaveText("Signal / Sea");
  await expect.poll(readinessCount).toBe(1);
  await expect(page.locator(".program-card")).toHaveCount(2);
  await expect(page.locator(".time-slot")).toHaveCount(6);
  await page.getByRole("button", { name: /Night block/ }).dblclick();
  await expect(page).toHaveURL(/player/);
  await expect(page.getByRole("img", { name: "Night archive" })).toBeVisible();
  await expect(page.locator(".player-toolbar, .player-title, .player-controls")).toHaveCount(0);
  await expect(page.locator(".player-viewport")).toHaveCSS("width", "1280px");
  await expect(page.locator(".player-viewport")).toHaveCSS("height", "720px");
});

test("viewer can tune channels with the legacy grid controls", async ({ page }) => {
  const readinessCount = await interceptBridge(page);
  await page.goto("/");
  await expect(page.locator(".splash-screen")).toBeHidden({ timeout: 6_000 });
  await expect.poll(readinessCount).toBe(1);
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
  await page.route("**/bridge/v1/presentation", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(presentation) }));
  await page.route("**/bridge/v1/ready", (route) => route.fulfill({ status: 204 }));
  await page.route("**/bridge/v1/media/**", (route) => route.fulfill({ status: 200, contentType: "image/png", body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP4z8DwH4QZGBgYGMAAAAD//wMAHikE/QAAAABJRU5ErkJggg==", "base64") }));
  await page.route("**/bridge/v1/remote/events**", async (route) => {
    polls += 1;
    if (polls === 1) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [], nextSequence: 0 }) });
    if (polls === 2) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [
      { sequence: 1, type: "key", key: "ChannelDown" },
      { sequence: 2, type: "key", key: "Guide" },
      { sequence: 3, type: "key", key: "ChannelNumber:1" },
    ], nextSequence: 3 }) });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [], nextSequence: 3 }) });
  });
  await page.goto("/");
  await expect(page).toHaveURL(/player/, { timeout: 6_000 });
  await expect(page.getByRole("img", { name: "Signal / Sea" })).toBeVisible();
});

test("viewer can retry a failed bridge load", async ({ page }) => {
  let attempts = 0;
  let readyCalls = 0;
  await page.route("**/bridge/v1/presentation", async (route) => {
    attempts += 1;
    if (attempts <= 2) {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "offline" }) });
      return;
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(livePresentation()) });
  });
  await page.route("**/bridge/v1/ready", async (route) => {
    readyCalls += 1;
    await route.fulfill({ status: 204 });
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Unable to load lineup" })).toBeVisible();
  await expect(page.locator(".splash-screen")).toBeHidden({ timeout: 6_000 });
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.locator(".header-title")).toHaveText("Signal / Sea");
  await expect.poll(() => readyCalls).toBe(1);
});
