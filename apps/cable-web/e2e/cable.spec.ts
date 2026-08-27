import { expect, test, type Page } from "@playwright/test";
import { presentationFixture } from "../src/presentation";

async function interceptBridge(page: Page) {
  let readyCalls = 0;
  await page.route("**/bridge/v1/presentation", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(presentationFixture) });
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

test("viewer can browse guide and open placard", async ({ page }) => {
  const readinessCount = await interceptBridge(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Tonight on Cable" })).toBeVisible();
  await expect.poll(readinessCount).toBe(1);
  await expect(page.getByRole("button", { name: /Signal \/ Sea/ })).toBeVisible();
  await page.getByRole("link", { name: "Placard", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Signal / Sea" })).toBeVisible();
  await page.getByRole("link", { name: "Diagnostics" }).click();
  await expect(page.getByRole("heading", { name: "Signal details" })).toBeVisible();
});

test("viewer can tune with keyboard and control player volume", async ({ page }) => {
  const readinessCount = await interceptBridge(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Tonight on Cable" })).toBeVisible();
  await expect.poll(readinessCount).toBe(1);
  const gallery = page.getByRole("button", { name: /Tune 2 GALL/ });
  await gallery.press("Enter");
  await expect(page.getByText("02 / GALL")).toBeVisible();
  await page.getByRole("button", { name: /Night archive/ }).click();
  await expect(page.getByRole("heading", { name: "Night archive" })).toBeVisible();
  const volume = page.locator("#cable-volume");
  await expect(volume).toHaveValue("100");
  await volume.fill("24");
  await expect(volume).toHaveValue("24");
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
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(presentationFixture) });
  });
  await page.route("**/bridge/v1/ready", async (route) => {
    readyCalls += 1;
    await route.fulfill({ status: 204 });
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Unable to load lineup" })).toBeVisible();
  await page.getByRole("button", { name: "Retry bridge" }).click();
  await expect(page.getByRole("heading", { name: "Tonight on Cable" })).toBeVisible();
  await expect.poll(() => readyCalls).toBe(1);
});
