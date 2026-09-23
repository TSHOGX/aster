import { expect, test } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

test("directory pages plateau while the selected file and unsaved edit stay owned", async ({
  page,
  context,
}) => {
  await workspaceFixture(page);
  await page.route("**/api/projects/battery/files?**", (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("path")) return route.fallback();
    const start = Number(url.searchParams.get("after") || 0);
    return route.fulfill({
      json: {
        entries: Array.from({ length: 100 }, (_, i) => ({
          name: `entry-${start + i}.txt`,
          kind: "file",
          size: 0,
        })),
        next: String(start + 100),
      },
    });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  await page.getByRole("button", { name: "entry-0.txt", exact: true }).click();
  await page.getByRole("button", { name: "File actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
  const edit = page.getByLabel("Edit entry-0.txt", { exact: true });
  await edit.fill("Unsaved across directory pages");
  const cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");
  const snapshots: {
    page: number;
    heap: number;
    nodes: number;
    listeners: number;
  }[] = [];
  for (let n = 1; n <= 50; n++) {
    await page
      .locator(".file-tree")
      .getByRole("button", { name: "Next page", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: `entry-${n * 100}.txt`, exact: true }),
    ).toBeAttached();
    await expect(page.locator(".file-row")).toHaveCount(100);
    if (n === 5 || n % 10 === 0) {
      await cdp.send("HeapProfiler.collectGarbage");
      const metrics = await cdp.send("Performance.getMetrics"),
        dom = await cdp.send("Memory.getDOMCounters");
      snapshots.push({
        page: n,
        heap: metrics.metrics.find((m) => m.name === "JSHeapUsedSize")!.value,
        nodes: dom.nodes,
        listeners: dom.jsEventListeners,
      });
    }
  }
  const output =
    process.env.DIRECTORY_PROFILE_OUTPUT ||
    test.info().outputPath("directory-profile");
  mkdirSync(output, { recursive: true });
  writeFileSync(
    resolve(output, "directory-window.json"),
    JSON.stringify(snapshots, null, 2),
  );
  const baseline = snapshots[0];
  for (const snapshot of snapshots.slice(1)) {
    expect(snapshot.heap).toBeLessThan(
      baseline.heap + Math.max(2 * 1024 * 1024, baseline.heap * 0.2),
    );
    expect(snapshot.nodes).toBeLessThan(baseline.nodes + 100);
    expect(snapshot.listeners).toBeLessThan(baseline.listeners + 50);
  }
  await cdp.detach();
  await expect(edit).toContainText("Unsaved across directory pages");
  await expect(
    page.getByText("The selected file is outside the loaded directory page.", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .locator(".file-tree")
    .getByRole("button", { name: "First page", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "entry-0.txt", exact: true }),
  ).toBeVisible();
  await expect(edit).toContainText("Unsaved across directory pages");
});

test("opening branches is bounded and collapsing releases their rows", async ({
  page,
}) => {
  await workspaceFixture(page);
  await page.route("**/api/projects/battery/files?**", (route) => {
    const path = new URL(route.request().url()).searchParams.get("path");
    return route.fulfill({
      json: {
        entries: path
          ? [{ name: "child.txt", kind: "file", size: 0 }]
          : Array.from({ length: 20 }, (_, i) => ({
              name: `branch-${i}`,
              kind: "directory",
              size: 0,
            })),
      },
    });
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("button", { name: "File preview", exact: true }).click();
  for (let n = 0; n < 16; n++) {
    await page
      .getByRole("button", { name: `branch-${n}`, exact: true })
      .click();
    await expect(
      page.locator(`[data-file-path="branch-${n}/child.txt"]`),
    ).toBeVisible();
  }
  await page.getByRole("button", { name: "branch-16", exact: true }).click();
  await expect(
    page.getByText("Close a directory before opening another.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.locator(".file-row")).toHaveCount(36);
  await page.getByRole("button", { name: "branch-0", exact: true }).click();
  await expect(
    page.locator('[data-file-path="branch-0/child.txt"]'),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "branch-16", exact: true }).click();
  await expect(
    page.locator('[data-file-path="branch-16/child.txt"]'),
  ).toBeVisible();
  await expect(page.locator(".file-row")).toHaveCount(36);
  await page.getByLabel("Search files", { exact: true }).fill("child");
  await expect(page.locator(".file-search-row")).toHaveCount(20);
  await expect(page.locator('[data-file-path="branch-19/child.txt"]')).toBeVisible();
  await page.getByLabel("Search files", { exact: true }).fill("");
  await expect(page.locator(".file-row")).toHaveCount(36);
});

test("closing a loading directory cancels its demand and reopening fetches it again", async ({
  page,
}) => {
  await workspaceFixture(page);
  let requests = 0,
    release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/projects/battery/files?**", async (route) => {
    const path = new URL(route.request().url()).searchParams.get("path");
    if (path !== "reports") return route.fallback();
    if (++requests === 1) await held;
    await route.fulfill({
      json: { entries: [{ name: "summary.md", kind: "file", size: 0 }] },
    });
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
    await expect(
      page.getByRole("button", { name: "summary.md", exact: true }),
    ).toBeVisible();
  } finally {
    release();
  }
});
