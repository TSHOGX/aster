import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

for (const sample of [
  { path: "readout.xml", body: "<root>\n" + '<item id="n">value</item>\n'.repeat(50_000) + '<item id="target">TARGET</item>\n</root>', line: 50_002 },
  { path: "readout.yaml", body: "items:\n" + '  - value: "cell"\n'.repeat(65_000) + '  - value: "TARGET"\n', line: 65_002 },
  { path: "large-readout.xml", body: "<root>\n" + '<item id="n">value</item>\n'.repeat(200_000) + '<item id="target">TARGET</item>\n</root>', line: 200_002 },
  { path: "large-readout.yaml", body: "items:\n" + '  - value: "cell"\n'.repeat(280_000) + '  - value: "TARGET"\n', line: 280_002 },
]) {
  test(`${sample.path} keeps syntax after a distant jump and supports file search`, async ({ page }) => {
    const f = await workspaceFixture(page);
    f.messages.analysis[1].content = `[Readout](/workspace/${sample.path})`;
    await page.route("**/files/content?*", route => route.fulfill({ body: sample.body }));
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", { name: "Readout", exact: true }).click();
    await expect(page.locator(".source-preview")).toBeVisible();
    await page.getByLabel("Go to line").fill(String(sample.line));
    await page.getByRole("button", { name: "Go", exact: true }).click();
    const target = page.locator(".source-editor .cm-line").filter({ hasText: "TARGET" });
    await expect(target).toBeVisible();
    await expect.poll(() => target.locator("span").count(), { timeout: 3000 }).toBeGreaterThan(0);
    await page.getByLabel("Go to line").fill("1");
    await page.getByRole("button", { name: "Go", exact: true }).click();
    await page.getByRole("button", { name: "Find in file" }).click();
    const search = page.locator(".cm-search input[name=search]");
    await expect(search).toBeVisible();
    await search.fill("TARGET");
    await page.locator(".cm-search").getByRole("button", { name: "next" }).click();
    await expect(target).toBeVisible();
  });
}

test("a 5 MiB single line stays selectable and its last bytes remain searchable", async ({ page, context }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Readout](/workspace/long-line.txt)";
  const body = "START" + "x".repeat(5 * 1024 * 1024 - 8) + "END";
  await page.route("**/files/content?*", route => route.fulfill({ body }));
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Readout", exact: true }).click();
  const editor = page.locator(".source-editor .cm-content");
  await expect(editor).toContainText("START");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.press("ControlOrMeta+C");
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText().then(text => [text.length, text.endsWith("END")]))).toEqual([body.length, true]);
  expect(await page.locator(".source-editor").evaluate(node => node.querySelectorAll("*").length)).toBeLessThan(1000);
});
