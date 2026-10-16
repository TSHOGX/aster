import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
const origin = "https://127.0.0.1:18443";
const digest = (data: Buffer) => `sha256:${createHash("sha256").update(data).digest("hex")}`;

for (const width of [390, 1440]) test(`draft comparison preserves generic content and clears unavailable evidence at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "studio-admin", password: "browser-fixture-password" } })).ok()).toBe(true);
  const account = await (await page.request.get("/api/auth/session")).json();
  const language = async (value: string, rendered = true) => {
    const previous = await (await page.request.get("/api/me/preferences")).json();
    expect((await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: previous.revision, language: value } })).ok()).toBe(true);
    if (rendered) {
      await page.evaluate(id => window.dispatchEvent(new StorageEvent("storage", { key: `account-appearance:${id}` })), account.id);
      await expect(page.locator("html")).toHaveAttribute("lang", value === "zh" ? "zh-Hans" : "en");
    }
  };
  await language("en", false);
  await page.goto("/volumes-studio/"); await page.getByRole("link", { name: "Sign in", exact: true }).click();
  const session = await (await page.request.get("/volumes-studio/api/v1/session")).json();
  const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Generic comparison ${crypto.randomUUID()}` } });
  expect(created.ok()).toBe(true); const library = await created.json();
  const grant = async (effect: string, revision: number) => {
    const response = await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action: "volume.read", effect, expected_revision: revision } });
    expect(response.ok(), await response.text()).toBe(true);
  };
  await grant("allow", 0);
  expect((await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action: "volume.author", effect: "allow", expected_revision: 0 } })).ok()).toBe(true);
  const root = `/volumes-studio/api/v1/libraries/${library.id}`;
  const get = async (suffix: string) => { const response = await page.request.get(root + suffix); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const post = async (suffix: string, data: object) => { const response = await page.request.post(root + suffix, { headers, data: { request_id: crypto.randomUUID(), ...data } }); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const branch = await post("/branches", { name: "Compare draft", base_commit: (await get("/main")).commit_id });
  let revision = 1, head = branch.head;
  const text = async (path: string, value: string) => {
    revision = (await post(`/branches/${branch.id}/draft/text`, { expected_head: head, expected_revision: revision, path, text: value })).revision;
  };
  const structure = async (action: string, path: string) => {
    revision = (await post(`/branches/${branch.id}/draft/structure`, { expected_head: head, expected_revision: revision, action, path })).revision;
  };
  const upload = async (path: string, data: Buffer) => {
    const value = await post("/uploads", { branch_id: branch.id, expected_revision: revision, path, size: String(data.length), digest: digest(data) });
    expect((await page.request.put(`${root}/uploads/${value.id}/chunks/0`, { headers: { ...headers, "Content-Type": "application/octet-stream", "X-Chunk-Digest": digest(data) }, data })).ok()).toBe(true);
    await post(`/uploads/${value.id}/complete`, { expected_revision: revision });
    revision = (await get(`/branches/${branch.id}`)).draft_revision;
  };
  const oldBinary = Buffer.from([0, 1, 2, 255]), binary = Buffer.from([0, 7, 8, 255]), large = Buffer.alloc((1 << 20) + 1, "x");
  await text("note.custom", "Committed generic text"); await text("kind", "Former file"); await upload("removed.bin", oldBinary);
  head = (await post(`/branches/${branch.id}/commits`, { expected_head: head, expected_revision: revision, message: "Generic baseline" })).commit_id;
  await text("note.custom", "Changed generic text"); await structure("remove", "kind"); await structure("mkdir", "kind");
  await structure("remove", "removed.bin"); await upload("mystery.data", binary); await upload("large.json", large);
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${branch.id}&view=changes`);
  const comparison = page.getByRole("region", { name: "Draft comparison", exact: true });
  const select = async (name: string) => comparison.getByRole("button", { name, exact: true }).click();
  await select("note.custom changed");
  await expect(comparison.getByLabel("File differences", { exact: true })).toContainText("Changed generic text");
  const download = async (name: string, expected: Buffer) => {
    const url = await comparison.getByRole("link", { name, exact: true }).getAttribute("href");
    const response = await page.request.get(url!); expect(response.ok()).toBe(true); expect(await response.body()).toEqual(expected);
    expect(response.headers()["content-disposition"]).toContain("attachment");
  };
  await download("Download committed file", Buffer.from("Committed generic text"));
  await download("Download saved draft file", Buffer.from("Changed generic text"));
  await select("mystery.data added");
  await expect(comparison).toContainText("Binary file: text preview unavailable.");
  await expect(comparison.getByLabel("File differences", { exact: true })).toHaveCount(0);
  await download("Download saved draft file", binary);
  await page.reload(); await expect(comparison).toContainText("Binary file: text preview unavailable.");
  let largeReads = 0;
  page.on("request", request => { if (request.url().includes("/draft/content?") && request.url().includes("path=large.json")) largeReads++; });
  await select("large.json added"); await expect(comparison).toContainText("File exceeds the 1 MiB text preview limit."); expect(largeReads).toBe(0);
  await download("Download saved draft file", large);
  await select("kind changed");
  await expect(comparison).toContainText("Directory"); await expect(comparison.getByRole("link", { name: "Download saved draft file", exact: true })).toHaveCount(0);
  await download("Download committed file", Buffer.from("Former file"));
  await select("removed.bin removed"); await expect(comparison).toContainText("File absent"); await download("Download committed file", oldBinary);

  // Superseded file reads cannot restore the prior selection's content.
  let arrived!: () => void, release!: () => void;
  const waiting = new Promise<void>(resolve => { arrived = resolve; }), held = new Promise<void>(resolve => { release = resolve; });
  await page.route(`**${root}/branches/${branch.id}/draft/content?**path=note.custom**`, async route => {
    const response = await route.fetch(); arrived(); await held; await route.fulfill({ response }).catch(() => {});
  });
  await select("note.custom changed"); await waiting; await select("mystery.data added");
  await expect(comparison).toContainText("Binary file: text preview unavailable."); release();
  await page.unroute(`**${root}/branches/${branch.id}/draft/content?**path=note.custom**`);
  await expect(comparison).not.toContainText("Changed generic text");
  await language("zh");
  await expect(page.getByRole("region", { name: "草稿比较", exact: true })).toContainText("二进制文件：无法显示文本预览。");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: test.info().outputPath(`generic-comparison-${width}.png`), fullPage: true });
  await language("en");
  await page.route(`**${root}/branches/${branch.id}/draft/diff?**`, route => route.abort("failed"));
  await comparison.getByRole("button", { name: "Reload comparison", exact: true }).click();
  await expect(comparison.getByRole("alert")).toContainText("Comparison is unavailable.");
  await expect(comparison.locator(".snapshot-metadata")).toHaveCount(0); await expect(comparison.locator(".change-list button")).toHaveCount(0);
  await page.unroute(`**${root}/branches/${branch.id}/draft/diff?**`);
  await comparison.getByRole("button", { name: "Reload comparison", exact: true }).click(); await expect(comparison).toContainText("Binary file: text preview unavailable.");
  await grant("deny", 1);
  await comparison.getByRole("button", { name: "Reload comparison", exact: true }).click();
  await expect(comparison.getByRole("alert")).toContainText("Comparison is unavailable.");
  await expect(comparison.locator(".snapshot-metadata")).toHaveCount(0); await expect(comparison.locator(".change-list button")).toHaveCount(0);
  await grant("allow", 2); await comparison.getByRole("button", { name: "Reload comparison", exact: true }).click();
  await expect(comparison).toContainText("Binary file: text preview unavailable.");
  expect(errors).toEqual([]);
});
