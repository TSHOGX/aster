import { expect, test } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

const fixtures = process.env.PREVIEW_FIXTURES;
const output = process.env.PREVIEW_PROFILE_OUTPUT;
test.skip(
  !fixtures || !output,
  "Requires generated Office fixtures and an evidence directory",
);

test("fifty preview, session, fullscreen and theme cycles release owned resources", async ({
  page,
  context,
}) => {
  test.setTimeout(180_000);
  const f = await workspaceFixture(page);
  const names =
    process.env.PREVIEW_CHARTS === "1"
      ? ["small.docx", "charts.xlsx", "charts.pptx"]
      : ["small.docx", "small.xlsx", "small.pptx"];
  f.messages.analysis[1].content = names
    .map((name) => `[${name}](/workspace/${name})`)
    .join("\n\n");
  await page.route("**/files/content?*", (route) => {
    const name = new URL(route.request().url()).searchParams.get("path")!;
    return names.includes(name)
      ? route.fulfill({ body: readFileSync(resolve(fixtures!, name)) })
      : route.fallback();
  });
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (message) => {
    if (message.type() === "log" && message.text().includes("workbook"))
      errors.push("The spreadsheet renderer logged its workbook model");
  });
  await page.addInitScript(() => {
    const urls = new Set<string>();
    const create = URL.createObjectURL.bind(URL),
      revoke = URL.revokeObjectURL.bind(URL);
    URL.createObjectURL = (blob) => {
      const url = create(blob);
      urls.add(url);
      return url;
    };
    URL.revokeObjectURL = (url) => {
      urls.delete(url);
      revoke(url);
    };
    Reflect.set(window, "fixtureLiveURLs", () => urls.size);
  });
  await page.goto("/?project=battery&session=analysis");
  await page
    .locator(".agent-panel textarea")
    .fill("Retained conversation draft");
  const cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");
  const snapshots: unknown[] = [];
  const snapshot = async (cycle: number) => {
    // The pinned spreadsheet viewer has a 200 ms debounced layout callback;
    // resource validation also coalesces file checks for 100 ms. Compare the
    // same quiescent state after both, not an immediately detached timer root.
    await page.waitForTimeout(500);
    await cdp.send("HeapProfiler.collectGarbage");
    const metrics = await cdp.send("Performance.getMetrics");
    const dom = await cdp.send("Memory.getDOMCounters");
    const resources = await page.evaluate(() => ({
      frames: document.querySelectorAll(".office-frame").length,
      urls: Reflect.get(window, "fixtureLiveURLs")(),
    }));
    const heap = metrics.metrics.find(
      (m) => m.name === "JSHeapUsedSize",
    )!.value;
    snapshots.push({ cycle, heap, dom, resources });
    mkdirSync(output!, { recursive: true });
    writeFileSync(
      resolve(output!, "lifecycle.json"),
      JSON.stringify({ snapshots, errors }, null, 2),
    );
    if (
      process.env.PREVIEW_HEAP_SNAPSHOTS === "1" &&
      (cycle === 0 || cycle === 40)
    ) {
      const chunks: string[] = [];
      const receive = ({ chunk }: { chunk: string }) => chunks.push(chunk);
      cdp.on("HeapProfiler.addHeapSnapshotChunk", receive);
      await cdp.send("HeapProfiler.takeHeapSnapshot", {
        reportProgress: false,
      });
      cdp.off("HeapProfiler.addHeapSnapshotChunk", receive);
      writeFileSync(
        resolve(output!, `cycle-${cycle}.heapsnapshot`),
        chunks.join(""),
      );
    }
    // Observe browser-owned frames/URLs/heap/listeners. The removed debug hook
    // is not part of the production contract; budget leases have unit coverage.
    expect(resources.frames).toBe(0);
    expect(resources.urls).toBe(1);
    return { heap, dom };
  };
  const cycle = async (i: number) => {
    await page
      .getByRole("link", { name: names[i % names.length], exact: true })
      .click();
    await expect(page.locator(".office-preview")).toHaveAttribute(
      "data-preview-phase",
      "ready",
    );
    if (names[i % names.length] === "charts.pptx") {
      const presentation = page.frameLocator(".office-frame");
      for (let slide = 0; slide < 3; slide++) {
        await presentation
          .getByRole("button", { name: "Next slide", exact: true })
          .click();
        await expect(presentation.locator(".chart-node svg")).toHaveCount(1);
        await presentation
          .getByRole("button", { name: "Previous slide", exact: true })
          .click();
        await expect(presentation.locator(".chart-node svg")).toHaveCount(1);
      }
    }
    {
      await page.getByLabel("Enter Full Screen", { exact: true }).click();
      await page.keyboard.press("Escape");
      await expect(
        page.getByLabel("Enter Full Screen", { exact: true }),
      ).toBeFocused();
      await page.getByRole("button", { name: "Account", exact: true }).click();
      await page.getByRole("menuitem", { name: "Theme", exact: true }).click();
      await page.getByLabel("Close file preview", { exact: true }).click();
      await page
        .getByRole("button", {
          name: "Prepare the research report",
          exact: true,
        })
        .click();
      await page
        .getByRole("button", { name: "Compare cycle performance", exact: true })
        .click();
      await expect(page.locator(".agent-panel textarea")).toHaveValue(
        "Retained conversation draft",
      );
      await page
        .getByRole("button", { name: "File preview", exact: true })
        .click();
    }
    await page.getByRole("button", { name: "README.md", exact: true }).click();
    await expect(page.locator(".file-markdown")).toContainText(
      "Battery research",
    );
    await expect(page.locator(".office-frame")).toHaveCount(0);
  };
  // Warm the complete journey, including React's first menu portal and the
  // second conversation's lazy mount, before comparing retained resources.
  for (let i = 0; i < 3; i++) await cycle(i);
  const baseline = await snapshot(0);
  for (let i = 0; i < 50; i++) {
    await cycle(i);
    if ((i + 1) % 10 === 0) {
      const current = await snapshot(i + 1);
      expect(current.heap).toBeLessThan(
        baseline.heap + Math.max(2 * 1024 * 1024, baseline.heap * 0.2),
      );
      expect(current.dom.nodes).toBeLessThan(baseline.dom.nodes + 300);
      expect(current.dom.jsEventListeners).toBeLessThan(
        baseline.dom.jsEventListeners + 30,
      );
    }
  }
  expect(errors).toEqual([]);
  await cdp.detach();
});
