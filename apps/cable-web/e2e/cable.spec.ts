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

test("viewer sees the legacy splash, browses the guide, and opens a programme", async ({ page }) => {
  const readinessCount = await interceptBridge(page);
  await page.goto("/");
  await expect(page.getByText("Chiba Cable Television", { exact: true })).toBeVisible();
  await expect(page.getByText("Chiba Cable Television", { exact: true })).toBeHidden({ timeout: 6_000 });
  await expect(page.locator(".header-title")).toHaveText("Signal / Sea");
  await expect.poll(readinessCount).toBe(1);
  await page.getByRole("button", { name: /Night archive/ }).click();
  await expect(page).toHaveURL(/player/);
  await expect(page.getByRole("heading", { name: "Night archive" })).toBeVisible();
  await expect(page.locator("#cable-volume")).toHaveValue("100");
});

test("viewer can tune channels with the legacy grid controls", async ({ page }) => {
  const readinessCount = await interceptBridge(page);
  await page.goto("/");
  await expect(page.locator(".splash-screen")).toBeHidden({ timeout: 6_000 });
  await expect.poll(readinessCount).toBe(1);
  const gallery = page.getByRole("button", { name: /02 Gallery GALL/ });
  await gallery.press("Enter");
  await expect(page.locator(".header-title")).toHaveText("Night archive");
  await page.getByRole("button", { name: /Night archive/ }).click();
  await expect(page).toHaveURL(/player/);
  await expect(page.getByRole("heading", { name: "Night archive" })).toBeVisible();
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
  await expect(page.locator(".header-title")).toHaveText("Signal / Sea");
  await expect.poll(() => readyCalls).toBe(1);
});
