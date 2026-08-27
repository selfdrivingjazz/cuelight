import { expect, test } from "@playwright/test";

test("operator can browse nodes and return to overview", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await page.getByRole("link", { name: "View nodes" }).click();
  await expect(page.getByRole("heading", { name: "Nodes" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Lobby display" })).toBeVisible();
  await page.getByRole("link", { name: "Lobby display" }).click();
  await expect(page.getByRole("heading", { name: "Lobby display" })).toBeVisible();
});
