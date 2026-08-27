import { expect, test } from "@playwright/test";

test("viewer can browse guide and open placard", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Tonight on Cable" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Signal \/ Sea/ })).toBeVisible();
  await page.getByRole("link", { name: "Placard" }).click();
  await expect(page.getByRole("heading", { name: "Signal / Sea" })).toBeVisible();
  await page.getByRole("link", { name: "Diagnostics" }).click();
  await expect(page.getByRole("heading", { name: "Signal details" })).toBeVisible();
});
