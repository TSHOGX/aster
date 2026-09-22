import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("delayed initial content never renders an empty conversation", async ({page}) => {
  await workspaceFixture(page);
  await page.route("**/sessions/analysis/snapshot?**", async route => {
    await new Promise(resolve => setTimeout(resolve, 3000));
    await route.fallback();
  });
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".agent-panel")).toBeVisible();
  await expect(page.locator(".agent-panel").getByRole("status")).toHaveText("Loading…");
  await expect(page.getByRole("heading",{name:"Start a conversation"})).toHaveCount(0);
  await expect(page.locator(".agent-panel")).toContainText("95% of their initial capacity");
  await expect(page.getByRole("heading",{name:"Start a conversation"})).toHaveCount(0);
});
for (const status of [202, 404, 500]) test(`initial ${status} is not empty`, async ({page}) => {
  await workspaceFixture(page);
  await page.route("**/sessions/analysis/snapshot?**", route => route.fulfill({status, contentType:"application/json",body:JSON.stringify({error:"Unavailable"})}));
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".agent-panel").getByRole("alert")).toBeVisible();
  await expect(page.getByRole("heading",{name:"Start a conversation"})).toHaveCount(0);
});
test("only successful empty content offers the empty state", async ({page}) => {
  const fixture = await workspaceFixture(page);
  fixture.messages.analysis = [];
  await page.goto("/?project=battery&session=analysis");
  await expect(page.getByRole("heading",{name:"Start a conversation"})).toBeVisible();
});
