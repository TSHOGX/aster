import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

const fixtures = process.env.PREVIEW_FIXTURES;
test.skip(!fixtures, "Requires generated Office fixtures");

for (const action of ["cancel", "timeout"]) {
  test(`${action} keeps the old preview and allows an explicit retry`, async ({
    page,
  }) => {
    const f = await workspaceFixture(page);
    f.messages.analysis[1].content = "[Document](/workspace/preview.pptx)";
    let body = readFileSync(resolve(fixtures!, "small.pptx"));
    await page.route("**/files/content?*", (route) => route.fulfill({ body }));
    let loads = 0,
      resume!: () => void;
    const gate = new Promise<void>((resolve) => {
      resume = resolve;
    });
    await page.route("**/office/*/presentation.html", async (route) => {
      if (++loads === 2) await gate;
      await route.continue().catch(() => {});
    });
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", { name: "Document", exact: true }).click();
    await expect(page.locator(".office-preview")).toHaveAttribute(
      "data-preview-phase",
      "ready",
    );
    await page
      .locator(".office-frame")
      .evaluate((frame) => frame.setAttribute("data-original", "true"));
    body = readFileSync(resolve(fixtures!, "large.pptx"));
    await page.evaluate(() =>
      window.dispatchEvent(
        new CustomEvent("fixture-file-event", {
          detail: {
            path: "preview.pptx",
            operation: "write",
            entry_type: "file",
          },
        }),
      ),
    );
    await expect.poll(() => loads).toBe(2);
    if (action === "cancel")
      await page
        .getByRole("button", { name: "Cancel preview", exact: true })
        .click();
    await expect(page.locator(".office-preview")).toHaveAttribute(
      "data-preview-phase",
      action === "cancel" ? "canceled" : "failed",
      { timeout: 35000 },
    );
    await expect(page.locator(".office-frame[data-original]")).toBeVisible();
    await expect(page.locator(".office-frame")).toHaveCount(1);
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.locator(".office-preview")).toHaveAttribute(
      "data-preview-phase",
      "ready",
    );
    await expect(
      page.frameLocator(".office-frame").locator(".office-frame-content"),
    ).toContainText("Measured slide 0");
    await expect(page.locator(".office-frame[data-original]")).toHaveCount(0);
    resume();
  });
}

for (const ext of ["pptx"]) {
  test(`prepared ${ext} replacement retains the displayed frame and ignores same bytes`, async ({
    page,
  }) => {
    const f = await workspaceFixture(page);
    const path = `preview.${ext}`;
    f.messages.analysis[1].content = `[Document](/workspace/${path})`;
    let body = readFileSync(
      resolve(fixtures!, ext === "docx" ? "small.docx" : `charts.${ext}`),
    );
    let reads = 0,
      frameLoads = 0;
    let resume!: () => void;
    const gate = new Promise<void>((resolve) => {
      resume = resolve;
    });
    await page.route("**/files/content?*", (route) => {
      reads++;
      return route.fulfill({ body });
    });
    await page.route("**/office/*/presentation.html", async (route) => {
      if (++frameLoads === 2) await gate;
      await route.continue().catch(() => {});
    });
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", { name: "Document", exact: true }).click();
    await expect(page.locator(".office-preview")).toHaveAttribute(
      "data-preview-phase",
      "ready",
    );
    await page.locator(".office-frame").evaluate((frame) => {
      frame.setAttribute("data-original", "true");
    });
    const emit = () =>
      page.evaluate(
        (path) =>
          window.dispatchEvent(
            new CustomEvent("fixture-file-event", {
              detail: { path, operation: "write", entry_type: "file" },
            }),
          ),
        path,
      );
    await page.waitForTimeout(300);
    const before = reads;
    await emit();
    await expect.poll(() => reads).toBeGreaterThan(before);
    await page.waitForTimeout(300);
    expect(frameLoads).toBe(1);
    body = readFileSync(
      resolve(
        fixtures!,
        ext === "docx" ? "large.docx" : `charts-updated.${ext}`,
      ),
    );
    await emit();
    await expect.poll(() => frameLoads).toBe(2);
    await expect(page.locator(".office-frame[data-original]")).toBeVisible();
    await expect(page.locator(".office-frame[aria-hidden=true]")).toHaveCount(
      1,
    );
    if (ext === "pptx") {
      const old = page.frameLocator(".office-frame[data-original]");
      await old
        .getByRole("button", { name: "Next slide", exact: true })
        .click();
      await expect(old.locator(".office-frame-content")).toContainText(
        "charts slide 1",
      );
      await expect(old.locator(".chart-node svg")).toHaveCount(1);
    }
    resume();
    await expect(page.locator(".office-preview")).toHaveAttribute(
      "data-preview-phase",
      "ready",
    );
    await expect(page.locator(".office-frame")).toHaveCount(1);
    await expect(page.locator(".office-frame[data-original]")).toHaveCount(0);
    if (ext === "pptx") {
      const current = page.frameLocator(".office-frame");
      await expect(current.locator(".office-frame-content")).toContainText(
        "charts-updated slide 1",
      );
      await expect(current.locator(".chart-node svg")).toHaveCount(1);
      await current
        .getByRole("button", { name: "Previous slide", exact: true })
        .focus();
      await page.keyboard.press("Enter");
      await expect(current.locator(".office-frame-content")).toContainText(
        "charts-updated slide 0",
      );
      await page.getByLabel("Enter Full Screen", { exact: true }).click();
      await current
        .getByRole("button", { name: "Next slide", exact: true })
        .focus();
      await page.keyboard.press("Escape");
      await expect(
        page.getByLabel("Enter Full Screen", { exact: true }),
      ).toBeFocused();
    }
    expect(errors).toEqual([]);
  });
}

test("a superseded frame request cannot replace a later text selection", async ({
  page,
}) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/small.pptx)";
  await page.route("**/files/content?*", (route) => {
    return new URL(route.request().url()).searchParams.get("path") ===
      "small.pptx"
      ? route.fulfill({ body: readFileSync(resolve(fixtures!, "small.pptx")) })
      : route.fallback();
  });
  let requested = false,
    resume!: () => void;
  const gate = new Promise<void>((resolve) => {
    resume = resolve;
  });
  await page.route("**/office/*/presentation.html", async (route) => {
    requested = true;
    await gate;
    await route.continue().catch(() => {});
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect.poll(() => requested).toBe(true);
  await page.getByRole("button", { name: "README.md", exact: true }).click();
  await expect(page.locator(".file-markdown")).toContainText(
    "Battery research",
  );
  resume();
  await expect(page.locator(".office-frame")).toHaveCount(0);
  // A fresh preview must still acquire admission after the canceled frame.
  // Internal budget accounting is tested in Vitest, not via a retired window hook.
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect(page.locator(".office-preview")).toHaveAttribute("data-preview-phase", "ready");
  await expect(page.locator(".office-frame")).toHaveCount(1);
  await page.getByRole("button", { name: "README.md", exact: true }).click();
  await expect(page.locator(".file-markdown")).toContainText("Battery research");
  await expect(page.locator(".office-frame")).toHaveCount(0);
});
