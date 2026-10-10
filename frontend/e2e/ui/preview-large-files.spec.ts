import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

// The headless shell omits the built-in PDF viewer; exercise full Chromium.
test.use({ channel: "chromium" });

function pdfFixture(size: number) {
  let pdf = "%PDF-1.4\n";
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    `<< /Length ${"BT /F1 24 Tf 50 700 Td (Battery report) Tj ET\n".length} >>\nstream\nBT /F1 24 Tf 50 700 Td (Battery report) Tj ET\nendstream`,
  ];
  const offsets = objects.map((body, index) => {
    const offset = pdf.length;
    pdf += `${index + 1} 0 obj\n${body}\nendobj\n`;
    return offset;
  });
  const trailer = (xref: number) => `xref\n0 6\n0000000000 65535 f \n${offsets.map(offset => `${String(offset).padStart(10, "0")} 00000 n \n`).join("")}trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n${String(xref).padStart(10, "0")}\n%%EOF\n`;
  // Pad a comment before the cross-reference table so EOF remains discoverable.
  const padding = size - pdf.length - trailer(0).length;
  pdf += "%" + " ".repeat(padding - 2) + "\n";
  return Buffer.from(pdf + trailer(pdf.length));
}

test("a 20 MiB PDF opens in the browser viewer without a limit error", async ({ page }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/report.pdf)";
  await page.route("**/files/content?*", route => route.fulfill({ body: pdfFixture(20 * 1024 * 1024) }));
  const violations: string[] = [];
  page.on("console", message => { if (/Refused|Content Security Policy/.test(message.text())) violations.push(message.text()); });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  const frame = page.locator(".pdf-preview iframe");
  await expect(frame).toBeVisible();
  await expect(frame).toHaveAttribute("src", /^blob:/);
  await expect(page.locator(".pdf-preview")).toContainText("20 MB");
  await expect.poll(() => page.frames().some(frame => frame.url().startsWith("blob:"))).toBe(true);
  await expect.poll(async () => {
    const viewer = page.frames().find(frame => frame.url().startsWith("chrome-extension://"));
    return viewer?.locator("pdf-viewer").evaluateAll(nodes =>
      nodes.some(node => Reflect.get(node, "documentDimensions")?.pageDimensions?.length === 1),
    );
  }).toBe(true);
  expect(violations).toEqual([]);
  await page.screenshot({ path: test.info().outputPath("pdf-preview.png") });
});

test("20 MiB text scrolls continuously with bounded visible DOM and leaves the workspace responsive", async ({ page }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/large.txt)";
  const prefix = "FIRST SECTION\n" + ("a".repeat(999) + "\n").repeat(1000);
  const text = (prefix + "SECOND SECTION\n" + ("b".repeat(999) + "\n").repeat(20000)).slice(0, 20 * 1024 * 1024);
  await page.route("**/files/content?*", route => route.fulfill({ body: text }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect(page.locator(".source-preview")).toBeVisible();
  await expect(page.locator(".source-editor .cm-content")).toContainText("FIRST SECTION");
  await page.getByLabel("Go to line").fill("1002");
  await page.getByRole("button", { name: "Go", exact: true }).click();
  await expect(page.locator(".source-editor .cm-content")).toContainText("SECOND SECTION");
  expect(await page.locator(".source-editor").evaluate(node => node.querySelectorAll("*").length)).toBeLessThan(1000);
  await page.locator(".agent-panel textarea").fill("Still working");
  await page.screenshot({ path: test.info().outputPath("large-text.png") });
});

test("browsers without a PDF viewer get a clear download fallback", async ({ page }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/report.pdf)";
  await page.addInitScript(() => Object.defineProperty(Navigator.prototype, "pdfViewerEnabled", { get: () => false }));
  await page.route("**/files/content?*", route => route.fulfill({ body: pdfFixture(1024) }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect(page.locator(".preview-status-card")).toContainText("Your browser cannot display PDFs here.");
  await expect(page.locator(".preview-status-card a")).toHaveAttribute("download", "");
  await expect(page.locator(".pdf-preview iframe")).toHaveCount(0);
});

for (const variant of [
  { locale: "en-US", theme: "light", width: 1600 },
  { locale: "zh-CN", theme: "dark", width: 390 },
] as const) {
  test(`oversized file recovery ${variant.locale} ${variant.theme}`, async ({ browser }) => {
    const context = await browser.newContext({ locale: variant.locale, colorScheme: variant.theme, reducedMotion: "reduce", viewport: { width: variant.width, height: 900 } });
    const page = await context.newPage();
    try {
      const f = await workspaceFixture(page);
      f.messages.analysis[1].content = "[Document](/workspace/long-research-report.pdf)";
      await page.route("**/files/content?*", route => route.fulfill({ body: "too big", headers: { "content-length": String(24 * 1024 * 1024) } }));
      await page.goto("/?project=battery&session=analysis");
      await page.getByRole("link", { name: "Document", exact: true }).click();
      const card = page.locator(".preview-status-card");
      await expect(card).toContainText(variant.locale === "zh-CN" ? "文件超出预览大小上限" : "File too large to preview");
      await expect(card).toContainText("20 MB");
      await expect(card).toContainText("24 MB");
      const download = card.getByRole("link");
      await expect(download).toHaveAttribute("download", "");
      await download.focus();
      await expect(download).toBeFocused();
      expect(await card.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
      await page.screenshot({ path: test.info().outputPath("oversize.png") });
    } finally { await context.close(); }
  });
}

test("a slow preview can be canceled and retried", async ({ page }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Document](/workspace/slow.txt)";
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  let requests = 0;
  await page.route("**/files/content?*", async route => {
    requests++;
    await pending;
    await route.fulfill({ body: "Ready after retry" }).catch(() => {});
  });
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Document", exact: true }).click();
  await expect.poll(() => requests).toBeGreaterThanOrEqual(1);
  await expect(page.locator(".preview-status-card progress")).toBeVisible();
  await page.getByRole("button", { name: "Cancel preview", exact: true }).click();
  await expect(page.locator(".preview-status-card")).toContainText("Preview canceled.");
  release();
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("fixture-file-event", { detail: { path: "slow.txt", operation: "write", entry_type: "file" } })));
  await expect(page.locator(".preview-status-card")).toContainText("Preview canceled.");
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("region", { name: "File slow.txt", exact: true })).toContainText("Ready after retry");
});
