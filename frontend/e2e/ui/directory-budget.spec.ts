import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

test("continuous directory windows keep DOM bounded and preserve the editor", async ({ page }) => {
  await workspaceFixture(page);
  const requests: number[] = [];
  await page.route("**/api/projects/battery/files?**", route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("path")) return route.fallback();
    const start = Number(url.searchParams.get("after") || 0);
    requests.push(start);
    return route.fulfill({ json: {
      snapshot: "directory-v1", start, total: 10_000,
      entries: Array.from({ length: 100 }, (_, i) => ({ name: `entry-${String(start + i).padStart(5, "0")}.txt`, kind: "file", size: 4 })),
      ...(start > 0 ? { previous: String(Math.max(0, start - 100)) } : {}),
      ...(start + 100 < 10_000 ? { next: String(start + 100) } : {}),
    } });
  });
  await page.route("**/files/content?*", route => route.fulfill({ body: "note" }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  await page.getByRole("button", { name: "entry-00000.txt", exact: true }).click();
  await page.getByRole("button", { name: "File actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
  const editor = page.getByLabel("Edit entry-00000.txt", { exact: true });
  await editor.fill("Unsaved across directory windows");
  const tree = page.locator(".virtual-file-tree");
  for (let pageNumber = 1; pageNumber <= 12; pageNumber++) {
    await tree.evaluate(node => { node.scrollTop = node.scrollHeight; });
    await expect.poll(() => requests.includes(pageNumber * 100)).toBe(true);
    expect(await tree.locator(".file-row").count()).toBeLessThan(100);
  }
  await expect(editor).toContainText("Unsaved across directory windows");
  // The virtual window renders its top row from the scroll event; repeat the
  // scroll in case it landed before the last page finished rendering.
  await expect(async () => {
    await tree.evaluate(node => { node.scrollTop = 0; node.dispatchEvent(new Event("scroll")); });
    await expect(tree.getByRole("button", { name: "Load previous files" })).toBeVisible({ timeout: 2000 });
  }).toPass({ timeout: 20000 });
  await tree.getByRole("button", { name: "Load previous files" }).click();
  await expect.poll(() => requests.includes(700)).toBe(true);
  await expect(editor).toContainText("Unsaved across directory windows");
});

test("twenty expanded directories remain open without rendering every row", async ({ page }) => {
  await workspaceFixture(page);
  await page.route("**/api/projects/battery/files?**", route => {
    const path = new URL(route.request().url()).searchParams.get("path");
    return route.fulfill({ json: { entries: path
      ? [{ name: "child.txt", kind: "file", size: 0 }]
      : Array.from({ length: 20 }, (_, i) => ({ name: `branch-${i}`, kind: "directory", size: 0 })) } });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  const tree = page.locator(".virtual-file-tree");
  for (let n = 0; n < 20; n++) {
    await tree.evaluate((node, index) => { node.scrollTop = Math.max(0, index * 72 - 100); }, n);
    await tree.getByRole("button", { name: `branch-${n}`, exact: true }).click();
    await expect(tree.locator(`[data-file-path="branch-${n}/child.txt"]`)).toBeVisible();
  }
  await expect(tree.getByRole("button", { name: "branch-19", exact: true })).toBeVisible();
  expect(await tree.locator(".file-row").count()).toBeLessThan(40);
  await tree.evaluate(node => { node.scrollTop = 0; });
  await tree.getByRole("button", { name: "branch-0", exact: true }).click();
  await expect(tree.locator('[data-file-path="branch-0/child.txt"]')).toHaveCount(0);
});

test("closing a loading directory cancels its demand and reopening fetches it again", async ({ page }) => {
  await workspaceFixture(page);
  let requests = 0, release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/projects/battery/files?**", async route => {
    const path = new URL(route.request().url()).searchParams.get("path");
    if (path !== "reports") return route.fallback();
    if (++requests === 1) await held;
    await route.fulfill({ json: { entries: [{ name: "summary.md", kind: "file", size: 0 }] } }).catch(() => {});
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  const reports = page.getByRole("button", { name: "reports", exact: true });
  try {
    await reports.click();
    await expect.poll(() => requests).toBe(1);
    await reports.click();
    await reports.click();
    await expect.poll(() => requests).toBe(2);
    await expect(page.getByRole("button", { name: "summary.md", exact: true })).toBeVisible();
  } finally { release(); }
});
