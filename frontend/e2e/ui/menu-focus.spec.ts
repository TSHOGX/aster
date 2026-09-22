import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("Escape dismisses the open file menu after a preview moves focus", async ({ page }) => {
  await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "the research report" }).click();
  await expect(page.locator(".file-markdown")).toBeVisible();
  const trigger = page.getByRole("button", { name: "File actions", exact: true });
  await trigger.click();
  await expect(page.getByRole("menu", { name: "File actions" })).toBeVisible();
  // Reproduce a renderer's asynchronous focus change without an outside click.
  await page.locator(".agent-panel textarea").evaluate((node) => (node as HTMLTextAreaElement).focus());
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu", { name: "File actions" })).toHaveCount(0);
  await expect(trigger).toBeFocused();
});
