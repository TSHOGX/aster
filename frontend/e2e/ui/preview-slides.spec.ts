import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

for (const count of [41, 200]) {
  test(`${count} slides can be opened and jumped to the final slide`, async ({ page }) => {
    const f = await workspaceFixture(page);
    f.messages.analysis[1].content = `[Deck](/workspace/${count}-slides.pptx)`;
    const bytes = readFileSync(resolve(process.env.PREVIEW_FIXTURES!, `${count}-slides.pptx`));
    await page.route("**/files/content?*", route => route.fulfill({ body: bytes }));
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", { name: "Deck", exact: true }).click();
    const frame = page.frameLocator(".office-frame");
    const navigation = frame.getByRole("navigation", { name: "Slides" });
    await expect(navigation).toContainText(`1 / ${count}`);
    await navigation.getByRole("spinbutton", { name: "Slide number" }).fill(String(count));
    await navigation.getByRole("button", { name: "Go to slide" }).click();
    await expect(navigation).toContainText(`${count} / ${count}`);
    await expect(frame.getByText(`Measured slide ${count - 1}`)).toBeVisible();
    await expect(navigation.getByRole("button", { name: "Next slide" })).toBeDisabled();
    await navigation.getByRole("button", { name: "Previous slide" }).press("ArrowLeft");
    await expect(navigation).toContainText(`${count - 1} / ${count}`);
  });
}

test("private deployed 200-slide sample reaches slide 200", async ({ page }) => {
  test.skip(!process.env.ACTUAL_PREVIEW_SAMPLES, "Requires the private captured corpus");
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Deck](/workspace/pptx_xl_001.pptx)";
  const bytes = readFileSync(resolve(process.env.ACTUAL_PREVIEW_SAMPLES!, "pptx_xl_001.pptx"));
  await page.route("**/files/content?*", route => route.fulfill({ body: bytes }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Deck", exact: true }).click();
  const navigation = page.frameLocator(".office-frame").getByRole("navigation", { name: "Slides" });
  await expect(navigation).toContainText("1 / 200");
  await navigation.getByRole("spinbutton", { name: "Slide number" }).fill("200");
  await navigation.getByRole("button", { name: "Go to slide" }).click();
  await expect(navigation).toContainText("200 / 200");
  await expect(page.locator('.office-preview[data-preview-phase="ready"]')).toBeVisible();
});
