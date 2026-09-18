import { expect, test } from "@playwright/test";

test("dynamic heights, prepend, streaming, jump, resizing and restoration", async ({
  page,
}) => {
  await page.goto("/perf-fixtures/virtual");
  await expect(page.locator('[data-row="199"]')).toBeVisible();
  await page.getByRole("button", { name: "Jump", exact: true }).click();
  const target = page.locator('[data-row="150"]');
  await expect(target).toBeInViewport();
  await expect
    .poll(async () => (await target.boundingBox())!.y)
    .toBeLessThan(400);
  await page.evaluate(
    () =>
      new Promise<void>((done) =>
        requestAnimationFrame(() => requestAnimationFrame(() => done())),
      ),
  );
  const before = (await target.boundingBox())!.y;

  await page.getByRole("button", { name: "Prepend" }).click();
  await page.waitForTimeout(500);
  await expect
    .poll(async () => Math.abs((await target.boundingBox())!.y - before))
    .toBeLessThanOrEqual(8);
  await page.getByRole("button", { name: "Toggle" }).click();
  await page.getByRole("button", { name: "Toggle" }).click();
  await expect(target).toBeInViewport();
  await expect
    .poll(async () => Math.abs((await target.boundingBox())!.y - before))
    .toBeLessThanOrEqual(8);
  await target.locator("summary").click();
  await page.getByRole("button", { name: "Resize" }).click();
  await expect(target).toBeInViewport();
  await page.getByRole("button", { name: "Stream" }).click();
  await expect(target).toBeInViewport();
  expect(await page.locator("[data-row]").count()).toBeLessThan(40);
});
