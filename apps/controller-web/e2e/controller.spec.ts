import { expect, test, type Page, type Route } from "@playwright/test";

const pageInfo = { nextCursor: null, previousCursor: null, hasMore: false };
const node = { id: "11111111-1111-1111-1111-111111111111", name: "Lobby display", credentialState: "active", desiredGeneration: 4, observedGeneration: 4 };
const deployment = { id: "22222222-2222-2222-2222-222222222222", name: "Atrium Cable", timezone: "America/New_York", theme: "gallery", guideHorizonSeconds: 86_400, guideDensity: "hour", version: 3, createdBy: "33333333-3333-3333-3333-333333333333", updatedBy: "33333333-3333-3333-3333-333333333333", createdAt: "2026-08-27T10:00:00Z", updatedAt: "2026-08-27T12:00:00Z" };

async function installControllerApi(page: Page) {
  await page.route("**/health", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ status: "ok", version: "browser-test" }) }));
  await page.route("**/api/v1/**", async (route: Route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1", "");
    const reply = (json: unknown, status = 200, headers: Record<string, string> = {}) => route.fulfill({ status, headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(json) });
    if (path === "/operator/me") return reply({ id: "operator-one", scopes: ["admin", "nodes:read", "cable:read", "cable:write"], capabilities: { "cable:deployments:create": true } });
    if (path === "/health") return reply({ status: "ok", version: "browser-test" });
    if (path === "/nodes") return reply({ items: [node], page: pageInfo });
    if (path === `/nodes/${node.id}`) return reply(node, 200, { ETag: '"node:4:4"' });
    if (path === `/nodes/${node.id}/status`) return reply({ eventEpoch: "44444444-4444-4444-4444-444444444444", sequence: 4, desiredGenerationSeen: 4, observedGeneration: 4, presentationKind: "cable", conditions: [] });
    if (path === "/cable/deployments") return reply({ items: [deployment], page: pageInfo });
    if (path === `/cable/deployments/${deployment.id}`) return reply(deployment, 200, { ETag: '"v3"' });
    return reply({ items: [], page: pageInfo });
  });
}

test("operator authenticates and browses fleet detail", async ({ page }) => {
  await installControllerApi(page);
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

test("operator browses Cable deployment hierarchy", async ({ page }) => {
  await installControllerApi(page);
  await page.goto("/");
  await page.getByRole("textbox", { name: "Operator token" }).fill("operator-token");
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("link", { name: "Deployments" }).click();
  await expect(page.getByRole("heading", { name: "Cable deployments" })).toBeVisible();
  await page.getByRole("link", { name: "Atrium Cable" }).click();
  await expect(page.getByRole("heading", { name: "Atrium Cable" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Channels" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Schedule" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Revisions" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Publish" })).toBeVisible();
});
