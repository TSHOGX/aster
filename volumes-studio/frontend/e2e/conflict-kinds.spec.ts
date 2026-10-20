import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
const origin = "https://127.0.0.1:18443";
const digest = (data: Buffer) => `sha256:${createHash("sha256").update(data).digest("hex")}`;

for (const width of [390, 1440]) test(`binary oversized and structural conflict choices preserve exact content at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  const errors: string[] = []; page.on("pageerror", error => errors.push(error.message));
  expect((await page.request.post("/api/auth/login", { headers: { Origin: origin }, data: { username: "studio-admin", password: "browser-fixture-password" } })).ok()).toBe(true);
  const previous = await (await page.request.get("/api/me/preferences")).json();
  expect((await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: previous.revision, language: "en" } })).ok()).toBe(true);
  await page.goto("/volumes-studio/"); await page.getByRole("link", { name: "Sign in", exact: true }).click();
  const session = await (await page.request.get("/volumes-studio/api/v1/session")).json();
  const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Conflict kinds ${crypto.randomUUID()}` } });
  expect(created.ok()).toBe(true); const library = await created.json();
  // Creating a library grants its creator every Studio action; no self-grant is needed.
  const root = `/volumes-studio/api/v1/libraries/${library.id}`;
  const get = async (suffix: string) => { const response = await page.request.get(root + suffix); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const post = async (suffix: string, data: object) => { const response = await page.request.post(root + suffix, { headers, data: { request_id: crypto.randomUUID(), ...data } }); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  type Branch = { id: string; head: string; draft_revision: number };
  const createBranch = async (name: string, base: string): Promise<Branch> => post("/branches", { name, base_commit: base });
  const structure = async (branch: Branch, action: string, path: string) => {
    const value = await post(`/branches/${branch.id}/draft/structure`, { expected_head: branch.head, expected_revision: branch.draft_revision, action, path, recursive: action === "remove" });
    branch.draft_revision = value.revision;
  };
  const text = async (branch: Branch, path: string, value: string) => {
    const result = await post(`/branches/${branch.id}/draft/text`, { expected_head: branch.head, expected_revision: branch.draft_revision, path, text: value });
    branch.draft_revision = result.revision;
  };
  const upload = async (branch: Branch, path: string, data: Buffer) => {
    const value = await post("/uploads", { branch_id: branch.id, expected_revision: branch.draft_revision, path, size: String(data.length), digest: digest(data) });
    const chunk = await page.request.put(`${root}/uploads/${value.id}/chunks/0`, { headers: { ...headers, "Content-Type": "application/octet-stream", "X-Chunk-Digest": digest(data) }, data });
    expect(chunk.ok(), await chunk.text()).toBe(true);
    await post(`/uploads/${value.id}/complete`, { expected_revision: branch.draft_revision });
    branch.draft_revision = (await get(`/branches/${branch.id}`)).draft_revision;
  };
  const checkpoint = async (branch: Branch) => {
    const value = await post(`/branches/${branch.id}/commits`, { expected_head: branch.head, expected_revision: branch.draft_revision, message: "Freeze conflict fixture" });
    branch.head = value.commit_id;
  };
  const accept = async (branch: Branch) => {
    const main = await get("/main");
    const value = await post("/integrations/prepare", { branch_id: branch.id, expected_head: branch.head, expected_main: main.commit_id, expected_policy_revision: 1 });
    await post("/integrations/reviews", { prepared_digest: value.digest, verdict: "approve" });
    return post("/integrations/merge", { prepared_digest: value.digest, expected_main: main.commit_id });
  };
  const base = await createBranch("Base", (await get("/main")).commit_id);
  await upload(base, "binary.bin", Buffer.from([0, 1, 255]));
  await upload(base, "large.txt", Buffer.alloc((1 << 20) + 1, "a"));
  await text(base, "deleted.md", "Original text"); await text(base, "replaced", "Original file");
  await structure(base, "mkdir", "directory"); await text(base, "directory/child.md", "Original child");
  await checkpoint(base); const baseline = await accept(base);
  const main = await createBranch("Main changes", baseline.accepted_commit), source = await createBranch("Source changes", baseline.accepted_commit);
  await upload(main, "binary.bin", Buffer.from([0, 2, 255])); await upload(main, "large.txt", Buffer.alloc((1 << 20) + 1, "b"));
  const markerExample = "<<<<<<< This is a legitimate source example\nMain keeps this document.";
  await text(main, "deleted.md", markerExample); await text(main, "replaced", "Modified file"); await text(main, "directory/child.md", "Modified child");
  await text(main, "unrelated.md", "Preserve independent human edit"); await checkpoint(main); await accept(main);
  let binary = Buffer.from([0, 3, 255, 42]);
  const large = Buffer.alloc((1 << 20) + 1, "c");
  await upload(source, "binary.bin", binary); await upload(source, "large.txt", large);
  await structure(source, "remove", "deleted.md"); await structure(source, "remove", "replaced");
  await structure(source, "mkdir", "replaced"); await text(source, "replaced/child.md", "Chosen directory child");
  await structure(source, "remove", "directory"); await checkpoint(source);

  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${source.id}`);
  await page.getByRole("button", { name: "Prepare integration", exact: true }).click();
  const conflicts = page.getByRole("region", { name: "Resolve merge conflicts", exact: true });
  await expect(conflicts).toBeVisible();
  const oldID = new URL(page.url()).searchParams.get("conflict")!;
  const oldWorkspace = await get(`/conflicts/${oldID}`);
  await conflicts.getByRole("button", { name: "binary.bin", exact: true }).click();
  await expect(conflicts).toContainText("Upload a replacement to this path");
  const previousBinary = binary; binary = Buffer.from([0, 77, 255, 5, 17]);
  await page.route(`**${root}/uploads/*/chunks/*`, route => route.abort("failed"));
  await conflicts.getByLabel("Replacement file", { exact: true }).setInputFiles({ name: "different-local-name.bin", mimeType: "application/octet-stream", buffer: binary });
  const replace = page.getByRole("dialog", { name: "Replace file?", exact: true });
  await expect(replace).toContainText("binary.bin");
  await replace.getByRole("button", { name: "Replace", exact: true }).press("Enter");
  await expect(conflicts.getByRole("alert")).toContainText("Replacement upload is not confirmed.");
  const unfinished = (await get(`/uploads?branch_id=${source.id}`)).uploads.filter((item: { state: string }) => item.state === "open");
  expect(unfinished).toHaveLength(1);
  const rejectedCommit = await page.request.post(`${root}/branches/${source.id}/commits`, { headers, data: { request_id: crypto.randomUUID(), expected_head: source.head, expected_revision: source.draft_revision, message: "Must not commit unfinished replacement" } });
  expect(rejectedCommit.status()).toBe(409);
  await page.unroute(`**${root}/uploads/*/chunks/*`);
  await conflicts.getByLabel("Replacement file", { exact: true }).setInputFiles({ name: "different-local-name.bin", mimeType: "application/octet-stream", buffer: binary });
  await expect(page.getByRole("status").filter({ hasText: "Replacement saved to the working draft." })).toBeVisible();
  expect((await get(`/uploads/${unfinished[0].id}`)).state).toBe("complete");
  await expect(conflicts).toHaveCount(0);
  const draftAfterUpload = await get(`/branches/${source.id}`);
  const replacedBytes = await page.request.get(`${root}/branches/${source.id}/draft/content?${new URLSearchParams({ path: "binary.bin", revision: String(draftAfterUpload.draft_revision) })}`);
  expect(replacedBytes.ok()).toBe(true); expect(await replacedBytes.body()).toEqual(binary);
  expect((await get(`/conflicts/${oldID}`)).source_commit).toBe(oldWorkspace.source_commit);
  const originalBytes = await page.request.get(`${root}/commits/${oldWorkspace.source_commit}/content?path=binary.bin`);
  expect(await originalBytes.body()).toEqual(previousBinary);
  expect(draftAfterUpload.head).toBe(oldWorkspace.source_commit);
  await page.getByLabel("Commit message", { exact: true }).fill("Reviewed binary replacement");
  await page.getByRole("button", { name: "Commit saved draft", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: "Committed saved revision" })).toBeVisible();
  expect((await get(`/conflicts/${oldID}`)).stale).toBe(true);
  await page.getByRole("button", { name: "Prepare integration", exact: true }).click();
  await expect(conflicts).toBeVisible();
  const id = new URL(page.url()).searchParams.get("conflict")!;
  expect(id).not.toBe(oldID);
  const workspace = await get(`/conflicts/${id}`);
  expect(workspace.conflicts.map((value: { path: string }) => value.path)).toEqual(expect.arrayContaining(["binary.bin", "large.txt", "deleted.md", "directory", "replaced"]));
  for (const item of workspace.conflicts) {
    await expect(conflicts.getByRole("button", { name: item.path, exact: true })).toBeEnabled();
    await conflicts.getByRole("button", { name: item.path, exact: true }).press("Enter");
    await expect(conflicts.getByRole("heading", { name: item.path, exact: true })).toBeVisible();
    const sides = conflicts.locator(".conflict-sides");
    if (item.path === "binary.bin" || item.path === "large.txt") {
      await expect(sides).toContainText(item.path === "binary.bin" ? "Binary file: text preview unavailable." : "File exceeds the 1 MiB text preview limit.");
      const branchSide = sides.locator("section").filter({ has: page.getByRole("heading", { name: "Branch candidate", exact: true }) });
      const link = branchSide.getByRole("link", { name: "Download exact file", exact: true });
      const download = await page.request.get((await link.getAttribute("href"))!);
      expect(download.ok()).toBe(true); expect(await download.body()).toEqual(item.path === "binary.bin" ? binary : large);
      expect(download.headers()["content-disposition"]).toContain("attachment");
    }
    if (item.path === "deleted.md") await expect(sides).toContainText("File absent");
    const choice = item.path === "deleted.md" ? "main" : item.path.startsWith("directory") ? "delete" : "branch";
    await conflicts.getByLabel("Resolution choice", { exact: true }).selectOption(choice);
    const result = conflicts.getByRole("region", { name: "Resolution result", exact: true });
    if (item.path === "deleted.md") await expect(result).toContainText(markerExample);
    if (item.path === "replaced") { await expect(result).toContainText("complete directory subtree"); await expect(conflicts.getByLabel("Replacement file", { exact: true })).toBeDisabled(); }
    if (item.path.startsWith("directory")) await expect(result).toContainText("This path and its descendants will be removed.");
    await conflicts.getByRole("button", { name: "Save resolution", exact: true }).click();
    await expect(conflicts.getByText("Unsaved resolution is retained in this tab.", { exact: true })).toHaveCount(0);
  }
  const account = await (await page.request.get("/api/auth/session")).json();
  const language = async (value: string) => {
    const prior = await (await page.request.get("/api/me/preferences")).json();
    expect((await page.request.patch("/api/me/preferences", { headers: { Origin: origin }, data: { expected_revision: prior.revision, language: value } })).ok()).toBe(true);
    await page.evaluate(id => window.dispatchEvent(new StorageEvent("storage", { key: `account-appearance:${id}` })), account.id);
    await expect(page.locator("html")).toHaveAttribute("lang", value === "zh" ? "zh-Hans" : "en");
  };
  await expect(conflicts.getByRole("button", { name: "binary.bin", exact: true })).toBeEnabled();
  await conflicts.getByRole("button", { name: "binary.bin", exact: true }).press("Enter");
  await language("zh");
  const translated = page.getByRole("region", { name: "解决合并冲突", exact: true });
  await expect(translated.getByRole("region", { name: "解决结果", exact: true })).toContainText("二进制文件：无法显示文本预览。");
  await page.screenshot({ path: test.info().outputPath(`conflict-kinds-${width}.png`), fullPage: true });
  await language("en");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await conflicts.getByRole("button", { name: "Prepare resolved integration", exact: true }).click();
  const review = page.getByRole("region", { name: "Integration review", exact: true });
  await review.getByRole("button", { name: "changed: binary.bin", exact: true }).click();
  const integrated = review.getByRole("region", { name: "Integrated file comparison", exact: true });
  for (const [side, expected] of [["Main before merge", Buffer.from([0, 2, 255])], ["Integrated result", binary]] as const) {
    const panel = integrated.getByRole("region", { name: side, exact: true });
    await expect(panel).toContainText("Binary file: text preview unavailable.");
    const response = await page.request.get((await panel.getByRole("link", { name: "Download exact file", exact: true }).getAttribute("href"))!);
    expect(response.ok()).toBe(true); expect(await response.body()).toEqual(expected);
  }
  await review.getByRole("button", { name: "changed: large.txt", exact: true }).click();
  await expect(integrated).toContainText("File exceeds the 1 MiB text preview limit.");
  await review.getByRole("button", { name: "changed: replaced", exact: true }).click();
  await expect(integrated.getByRole("region", { name: "Integrated result", exact: true })).toContainText("Directory");
  await expect(integrated.getByRole("region", { name: "Integrated result", exact: true }).getByRole("link")).toHaveCount(0);
  await review.getByRole("button", { name: "removed: directory", exact: true }).click();
  await expect(integrated.getByRole("region", { name: "Integrated result", exact: true })).toContainText("File absent");
  const integrationDigest = new URL(page.url()).searchParams.get("integration")!;
  await page.route(`**${root}/integrations/*/file?**`, route => route.abort("failed"));
  await review.getByRole("button", { name: "changed: binary.bin", exact: true }).click();
  await expect(review.getByRole("alert")).toContainText("Integration evidence is unavailable.");
  await expect(integrated).toHaveCount(0); await expect(review.getByRole("button", { name: "Approve integration", exact: true })).toHaveCount(0);
  await page.unroute(`**${root}/integrations/*/file?**`);
  await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
  await review.getByRole("button", { name: "changed: binary.bin", exact: true }).click();
  await expect(integrated).toContainText("Binary file: text preview unavailable.");
  const access = async (effect: string, revision: number) => expect((await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action: "volume.read", effect, expected_revision: revision } })).ok()).toBe(true);
  await access("deny", 1);
  await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
  await expect(review.getByRole("alert")).toContainText("Integration evidence is unavailable.");
  await expect(integrated).toHaveCount(0); await expect(review.getByRole("button", { name: "Approve integration", exact: true })).toHaveCount(0);
  expect(new URL(page.url()).searchParams.get("integration")).toBe(integrationDigest);
  await access("allow", 2);
  await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
  await review.getByRole("button", { name: "changed: binary.bin", exact: true }).click();
  await language("zh");
  await expect(page.getByRole("region", { name: "集成文件比较", exact: true })).toContainText("二进制文件：无法显示文本预览。");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: test.info().outputPath(`integrated-fallback-${width}.png`), fullPage: true });
  await language("en");
  await page.getByRole("button", { name: "Approve integration", exact: true }).click();
  await page.getByRole("button", { name: "Merge approved integration", exact: true }).click();
  await expect(page.getByText("Acceptance is complete. Publishing creates a version; it does not deploy to Projects.")).toBeVisible();
  const accepted = await get("/main");
  const version = await post("/versions", { accepted_commit: accepted.commit_id, label: "Resolved kinds" });
  await expect.poll(async () => (await get(`/versions/${version.id}`)).state).toBe("published");
  for (const [path, expected] of [["binary.bin", binary], ["large.txt", large], ["deleted.md", Buffer.from(markerExample)], ["replaced/child.md", Buffer.from("Chosen directory child")], ["unrelated.md", Buffer.from("Preserve independent human edit")]] as const) {
    const response = await page.request.get(`${root}/versions/${version.id}/content?${new URLSearchParams({ path })}`);
    expect(response.ok()).toBe(true); expect(await response.body()).toEqual(expected);
  }
  const tree = await get(`/versions/${version.id}/tree`);
  expect(tree.entries.map((entry: { path: string }) => entry.path)).toEqual(["binary.bin", "deleted.md", "large.txt", "replaced", "replaced/child.md", "unrelated.md"]);
  expect(tree.entries.find((entry: { path: string }) => entry.path === "replaced").kind).toBe("directory");
  expect(errors).toEqual([]);
});
