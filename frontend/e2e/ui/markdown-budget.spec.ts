import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";
import { conversationRoute } from "./conversation-fixture";

test.use({ acceptDownloads: true });

const section =
  "## Measured section\n\nBattery **capacity** and 中文 measurements.\n\n| Sample | Value |\n| --- | --- |\n| A | 100 |\n| B | 95 |\n\n";
function markdownBytes(size: number) {
  const repeat = Math.floor((size - 100) / Buffer.byteLength(section));
  return (
    "# Measured document\n\n" +
    section.repeat(repeat) +
    "\n\nComplete final line."
  );
}

async function readingReady(page: Page, text: string) {
  // Large files intentionally expose one bounded section, not the entire DOM.
  if (Buffer.byteLength(text) > 256 * 1024)
    await expect(page.locator(".large-text-preview .preview-text-page")).toContainText("Measured document");
  else
    await expect(page.locator(".file-markdown")).toContainText("Complete final line.");
}

test("plain conversation rendering keeps complete answer copy outside the excerpt", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const fixture = await workspaceFixture(page);
  const source = markdownBytes(128 * 1024);
  await conversationRoute(page, route => route.fulfill({ body: JSON.stringify({
    session: fixture.sessions[0],
    messages: [{ id: "answer", role: "assistant", content: source, sequence: 1, turn_id: "t", created_at: "2026-09-23T00:00:00Z" }],
    turn_summaries: [{ id: "t", state: "completed", final_message_id: "answer" }],
    timeline: [{ id: "answer", message_id: "answer", kind: "assistant", turn_id: "t", cursor: 1, content: source.slice(0, 16000), truncated: true }],
  }) }));
  await page.goto("/?project=battery&session=analysis");
  await expect(page.locator(".turn-final .markdown-plain")).toHaveText(source.slice(0, 16000));
  await page.getByRole("button", { name: "Copy answer", exact: true }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(source);
});
for (const size of [8 * 1024, 16 * 1024, 64 * 1024, 256 * 1024, 1024 * 1024]) {
  test(`Markdown preview CPU profile ${size}`, async ({ page, context }) => {
    const output = process.env.MARKDOWN_PROFILE_OUTPUT;
    test.skip(!output, "Requires a new profile output directory");
    test.setTimeout(60000);
    const f = await workspaceFixture(page);
    const text = markdownBytes(size);
    f.messages.analysis[1].content =
      "[Measured document](/workspace/measured.md)";
    await page.route("**/files/content?*", (route) =>
      route.fulfill({ body: text }),
    );
    await page.addInitScript(() => {
      const tasks: number[] = [];
      new PerformanceObserver((list) =>
        tasks.push(...list.getEntries().map((entry) => entry.duration)),
      ).observe({ type: "longtask" });
      Reflect.set(window, "markdownTasks", tasks);
    });
    await page.goto("/?project=battery&session=analysis");
    await expect(
      page.getByRole("link", { name: "Measured document", exact: true }),
    ).toBeVisible();
    const cdp = await context.newCDPSession(page);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
    await cdp.send("Profiler.enable");
    await cdp.send("Profiler.start");
    await page.evaluate(() => {
      Reflect.get(window, "markdownTasks").length = 0;
    });
    const start = performance.now();
    await page
      .getByRole("link", { name: "Measured document", exact: true })
      .click();
    await readingReady(page, text);
    const complete = performance.now() - start;
    await page.waitForTimeout(250);
    const profile = await cdp.send("Profiler.stop");
    await cdp.send("HeapProfiler.collectGarbage");
    const dom = await cdp.send("Memory.getDOMCounters");
    const tasks = await page.evaluate(() =>
      Reflect.get(window, "markdownTasks"),
    );
    mkdirSync(output!, { recursive: true });
    writeFileSync(
      resolve(output!, `${size}.json`),
      JSON.stringify(
        { bytes: Buffer.byteLength(text), readiness: "first readable section", complete, dom, tasks },
        null,
        2,
      ),
    );
    writeFileSync(
      resolve(output!, `${size}.cpuprofile`),
      JSON.stringify(profile.profile),
    );
    await cdp.detach();
  });
}

for (const scenario of ["large", "dense"]) {
  test(`${scenario} Markdown stays complete and editable through the plain preview`, async ({
    page,
  }) => {
    const f = await workspaceFixture(page);
    const body =
      scenario === "large"
        ? markdownBytes(1024 * 1024)
        : "[".repeat(4000) + "safe literal <script>bad()</script>";
    f.files["reports/summary.md"] = body;
    await page.goto("/?project=battery&session=analysis");
    await page
      .getByRole("link", { name: "the research report", exact: true })
      .click();
    if (scenario === "large") {
      const preview = page.locator(".large-text-preview");
      await expect(preview).toBeVisible();
      const next = preview.getByRole("button", { name: "Next section", exact: true });
      let complete = "";
      do {
        const section = await preview.locator(".preview-text-page code").textContent();
        expect(section!.length).toBeLessThanOrEqual(65536);
        complete += section;
        if (await next.isDisabled()) break;
        await next.click();
      } while (true);
      expect(complete).toBe(body);
      await expect(preview.locator("table,script,img")).toHaveCount(0);
    } else {
      const preview = page.locator(".file-markdown");
      await expect(preview.locator("[data-markdown-mode=plain]")).toBeVisible();
      expect(await preview.locator(".markdown-plain").textContent()).toBe(body);
      await expect(preview.locator("table,script,img")).toHaveCount(0);
      await preview.locator(".markdown-plain").evaluate((node) => {
        const range = document.createRange();
        range.selectNodeContents(node);
        const selection = window.getSelection()!;
        selection.removeAllRanges();
        selection.addRange(range);
      });
      expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(body);
    }
    await page.getByLabel("File actions", { exact: true }).click();
    await page
      .getByRole("menuitem", { name: "Edit text", exact: true })
      .click();
    const editor = page.getByLabel("Edit reports/summary.md", { exact: true });
    await expect(editor).toBeVisible();
    await editor.fill("Keep this unsaved text");
    await page.getByLabel("Close file preview", { exact: true }).click();
    await page
      .getByRole("button", { name: "File preview", exact: true })
      .click();
    await expect(editor).toContainText("Keep this unsaved text");
  });
}

for (const size of [8 * 1024, 64 * 1024, 1024 * 1024]) {
  test(`thirty independent warm-application Markdown journeys ${size}`, async ({
    browser,
  }) => {
    const output = process.env.MARKDOWN_LATENCY_OUTPUT;
    test.skip(!output, "Requires a new latency output directory");
    test.setTimeout(180000);
    const samples: { ms: number; status: string; error?: string }[] = [];
    const text = markdownBytes(size);
    // A fresh context per trial removes application cache/representation reuse.
    // The app is ready before each intent. Server/filesystem caches are warm;
    // Playwright routing disables the browser HTTP cache in each fresh context.
    for (let i = -1; i < 30; i++) {
      const context = await browser.newContext({
        viewport: { width: 1600, height: 1000 },
      });
      const page = await context.newPage();
      let began = 0;
      try {
        const f = await workspaceFixture(page);
        f.messages.analysis[1].content =
          "[Measured document](/workspace/measured.md)";
        await page.route("**/files/content?*", (route) =>
          route.fulfill({ body: text }),
        );
        const cdp = await context.newCDPSession(page);
        await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
        await page.goto("/?project=battery&session=analysis");
        const link = page.getByRole("link", {
          name: "Measured document",
          exact: true,
        });
        await expect(link).toBeVisible();
        began = performance.now();
        await link.click();
        await readingReady(page, text);
        if (i >= 0)
          samples.push({ ms: performance.now() - began, status: "ready" });
      } catch (error) {
        if (i >= 0)
          samples.push({
            ms: began ? performance.now() - began : 0,
            status: "failed",
            error: String(error),
          });
        else throw error;
      } finally {
        await context.close();
        mkdirSync(output!, { recursive: true });
        const sorted = samples
          .filter((s) => s.status === "ready")
          .map((s) => s.ms)
          .sort((a, b) => a - b);
        writeFileSync(
          resolve(output!, `${size}.json`),
          JSON.stringify(
            {
              bytes: Buffer.byteLength(text),
              readiness: "first readable section",
              cpuRate: 4,
              warmups: 1,
              samples,
              failures: samples.filter((s) => s.status !== "ready").length,
              quantile: "nearest rank",
              p50: sorted[Math.ceil(sorted.length * 0.5) - 1],
              p95: sorted[Math.ceil(sorted.length * 0.95) - 1],
              max: sorted.at(-1),
            },
            null,
            2,
          ),
        );
      }
    }
    expect(samples).toHaveLength(30);
    expect(samples.filter((s) => s.status !== "ready")).toEqual([]);
  });
}
