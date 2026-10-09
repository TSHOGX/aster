import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("an expired cursor seeks around the visible anchor without replaying a directory scan", async ({ page }) => {
  await workspaceFixture(page);
  let expired = false;
  const seeks: string[] = [];
  await page.route("**/api/projects/battery/files?**", route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("path")) return route.fallback();
    const cursor = url.searchParams.get("after");
    if (cursor === "expired") {
      expired = true;
      return route.fulfill({ status: 409, json: { code: "resync_required", error: "resource requires resync" } });
    }
    const seek = url.searchParams.get("seek");
    const name = url.searchParams.get("name") ?? "";
    if (seek) seeks.push(name);
    const index = Number(/entry-(\d+)/.exec(name)?.[1] ?? 0);
    const start = seek ? Math.max(0, index - 50) : Number(cursor ?? 0);
    return route.fulfill({ json: {
      snapshot: expired ? "new" : "old", start, total: 1000,
      entries: Array.from({ length: 100 }, (_, i) => ({ name: `entry-${String(start + i).padStart(3, "0")}.txt`, kind: "file", size: 0 })),
      next: expired ? String(start + 100) : "expired",
      ...(start > 0 ? { previous: String(Math.max(0, start - 100)) } : {}),
    } });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  const tree = page.locator(".virtual-file-tree");
  await expect(tree.getByRole("button", { name: "entry-000.txt", exact: true })).toBeVisible();
  await tree.evaluate(node => { node.scrollTop = node.scrollHeight; });
  await expect.poll(() => expired).toBe(true);
  await expect.poll(() => seeks.length).toBe(1);
  expect(Number(/entry-(\d+)/.exec(seeks[0])?.[1])).toBeGreaterThan(50);
  await expect(tree.getByRole("button", { name: seeks[0], exact: true })).toBeVisible();
  await expect(page.locator(".files-panel")).not.toContainText("resource requires resync");
  expect(await tree.locator(".file-row").count()).toBeLessThan(100);
});

test("a deep link reveals a distant filename using bounded seek windows", async ({ page }) => {
  const fixture = await workspaceFixture(page);
  fixture.messages.analysis[1].content = "[Document](/workspace/entry-90000.txt)";
  const requests: URL[] = [];
  await page.route("**/api/projects/battery/files?**", route => {
    const url = new URL(route.request().url());
    requests.push(url);
    const start = url.searchParams.get("seek") === "around" ? 89950 : 0;
    return route.fulfill({ json: { snapshot: "files", start, total: 100000,
      entries: Array.from({ length: 100 }, (_, i) => ({ name: `entry-${String(start + i).padStart(5, "0")}.txt`, kind: "file", size: 4 })),
    } });
  });
  await page.route("**/files/content?*", route => route.fulfill({ body: "text" }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect(page.locator('.virtual-file-tree [data-file-path="entry-90000.txt"]')).toBeVisible();
  const seeks = requests.filter(url => url.searchParams.get("seek") === "around");
  expect(new Set(seeks.map(url => url.searchParams.get("name")))).toEqual(new Set(["entry-90000.txt"]));
  expect(seeks.length).toBeLessThanOrEqual(2); // Initial watch validation may refresh the observation.
  expect(requests.some(url => url.searchParams.get("after"))).toBe(false);
});
