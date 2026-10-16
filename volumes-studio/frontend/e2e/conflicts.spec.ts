import { test, expect } from "@playwright/test";
const origin = "https://127.0.0.1:18443";

for (const width of [390, 1440]) test(`conflict edits and exact operations recover across authority and concurrent edits at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  page.on("dialog", dialog => dialog.accept());
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
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Conflict recovery ${crypto.randomUUID()}` } });
  expect(created.ok()).toBe(true); const library = await created.json();
  const grant = async (action: string, effect: string, revision: number) => {
    const response = await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action, effect, expected_revision: revision } });
    expect(response.ok(), await response.text()).toBe(true);
  };
  for (const action of ["volume.read", "volume.author", "volume.review", "volume.merge", "volume.publish"]) await grant(action, "allow", 0);
  const root = `/volumes-studio/api/v1/libraries/${library.id}`;
  const get = async (suffix: string) => { const response = await page.request.get(root + suffix); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const post = async (suffix: string, data: object) => { const response = await page.request.post(root + suffix, { headers, data: { request_id: crypto.randomUUID(), ...data } }); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const main = await get("/main");
  const branch = async (name: string) => {
    const value = await post("/branches", { name, base_commit: main.commit_id });
    await post(`/branches/${value.id}/draft/text`, { expected_head: value.head, expected_revision: 1, path: "lesson.md", text: name });
    const commit = await post(`/branches/${value.id}/commits`, { expected_head: value.head, expected_revision: 2, message: name });
    return { ...value, head: commit.commit_id };
  };
  const first = await branch("Accepted main text"), candidate = await branch("Conflicting branch text");
  const prepared = await post("/integrations/prepare", { branch_id: first.id, expected_head: first.head, expected_main: main.commit_id, expected_policy_revision: 1 });
  await post("/integrations/reviews", { prepared_digest: prepared.digest, verdict: "approve" });
  await post("/integrations/merge", { prepared_digest: prepared.digest, expected_main: main.commit_id });
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${candidate.id}`);
  await page.getByRole("button", { name: "Prepare integration", exact: true }).press("Enter");
  const conflicts = page.getByRole("region", { name: "Resolve merge conflicts", exact: true });
  await conflicts.getByRole("button", { name: "lesson.md", exact: true }).press("Enter");
  await expect(conflicts).toContainText("Accepted main text"); await expect(conflicts).toContainText("Conflicting branch text");
  const workspaceID = new URL(page.url()).searchParams.get("conflict")!;
  const workspacePath = `/conflicts/${workspaceID}`;
  const recoveryKey = `studio-conflict:${JSON.stringify([session.issuer, session.subject, library.id, workspaceID])}`;
  // A command without a request identity must never be hydrated as a recoverable save.
  await page.evaluate(({ key, id }) => {
    const draft = { path: "lesson.md", choice: "text", text: "Invalid request must not run", expected_revision: 1 };
    sessionStorage.setItem(key, JSON.stringify({ workspace: id, selected: "lesson.md", draft, pending: { action: "save", draft } }));
  }, { key: recoveryKey, id: workspaceID });
  await conflicts.getByRole("button", { name: "Reload conflict workspace", exact: true }).click();
  await expect(conflicts.getByRole("alert")).toContainText("Local conflict recovery is invalid.");
  await expect(conflicts.getByRole("button", { name: "Recover conflict operation", exact: true })).toHaveCount(0);
  await expect(conflicts.getByRole("button", { name: "Prepare resolved integration", exact: true })).toBeDisabled();
  await conflicts.getByRole("button", { name: "Discard conflict recovery", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Discard", exact: true }).press("Enter");
  await conflicts.getByRole("button", { name: "lesson.md", exact: true }).click();
  await conflicts.getByLabel("Resolution choice", { exact: true }).selectOption("text");
  // A failed storage write must not restore an older persisted draft over newer in-memory text.
  await conflicts.getByLabel("Resolved text", { exact: true }).fill("Older persisted resolution");
  await expect(page.getByRole("button", { name: "Prepare integration", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Reload integration review", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Prepare selected composition", exact: true })).toBeDisabled();
  await page.evaluate(() => {
    const original = Storage.prototype.setItem;
    Object.assign(window, { restoreConflictStorage: () => { Storage.prototype.setItem = original; } });
    Storage.prototype.setItem = function (key, value) { if (key.startsWith("studio-conflict:")) throw new Error("storage unavailable"); return original.call(this, key, value); };
  });
  await conflicts.getByLabel("Resolved text", { exact: true }).fill("Newer in-memory resolution");
  await expect(conflicts.getByRole("alert")).toContainText("Local conflict recovery is unavailable.");
  await conflicts.getByRole("button", { name: "Reload conflict workspace", exact: true }).click();
  await expect(conflicts.getByLabel("Resolved text", { exact: true })).toHaveValue("Newer in-memory resolution");
  await expect(page.getByRole("button", { name: "Prepare integration", exact: true })).toBeDisabled();
  await page.evaluate(() => (window as unknown as { restoreConflictStorage: () => void }).restoreConflictStorage());
  await conflicts.getByLabel("Resolved text", { exact: true }).fill("My retained resolution");
  await page.reload();
  await expect(conflicts.getByLabel("Resolved text", { exact: true })).toHaveValue("My retained resolution");
  await expect(conflicts.getByRole("button", { name: "Prepare resolved integration", exact: true })).toBeDisabled();
  await grant("volume.read", "deny", 1);
  await conflicts.getByRole("button", { name: "Reload conflict workspace", exact: true }).click();
  await expect(conflicts).toContainText("Conflict evidence is unavailable.");
  await expect(conflicts.locator("pre")).toHaveCount(0); await expect(conflicts.getByLabel("Resolved text", { exact: true })).toHaveCount(0);
  await grant("volume.read", "allow", 2);
  await conflicts.getByRole("button", { name: "Reload conflict workspace", exact: true }).click();
  await expect(conflicts.getByLabel("Resolved text", { exact: true })).toHaveValue("My retained resolution");
  let lostSave = false;
  const saves: string[] = [];
  await page.route(`**${root}${workspacePath}/resolutions`, async route => {
    saves.push(route.request().postData()!);
    if (!lostSave) { const response = await route.fetch(); expect(response.ok()).toBe(true); lostSave = true; return route.abort("failed"); }
    return route.continue();
  });
  await conflicts.getByRole("button", { name: "Save resolution", exact: true }).click();
  await expect(conflicts.getByRole("alert")).toContainText("Conflict operation is unconfirmed.");
  await page.reload();
  await expect(conflicts.getByLabel("Resolved text", { exact: true })).toBeDisabled();
  await conflicts.getByRole("button", { name: "Recover conflict operation", exact: true }).click();
  await expect(conflicts.getByText("Unsaved resolution is retained in this tab.", { exact: true })).toHaveCount(0);
  expect(saves).toHaveLength(2); expect(saves[0]).toBe(saves[1]); expect((await get(workspacePath)).revision).toBe(2);
  await page.unroute(`**${root}${workspacePath}/resolutions`);

  await conflicts.getByLabel("Resolved text", { exact: true }).fill("My final reviewed resolution");
  await post(`${workspacePath}/resolutions`, { expected_revision: 2, path: "lesson.md", choice: "text", text: "Concurrent reviewer resolution" });
  await conflicts.getByRole("button", { name: "Save resolution", exact: true }).click();
  await expect(conflicts.getByRole("alert")).toContainText("Conflict workspace changed.");
  await conflicts.getByRole("button", { name: "Reload conflict workspace", exact: true }).click();
  await expect(conflicts.getByLabel("Resolved text", { exact: true })).toHaveValue("My final reviewed resolution");
  await expect(conflicts.getByRole("button", { name: "Save resolution", exact: true })).toBeDisabled();
  await conflicts.getByText("Current saved resolution", { exact: true }).click();
  await expect(conflicts.locator("details")).toContainText("Concurrent reviewer resolution");
  await conflicts.getByRole("button", { name: "Use current conflict revision", exact: true }).click();
  await conflicts.getByRole("button", { name: "Save resolution", exact: true }).press("Enter");
  await expect(conflicts.getByText("Unsaved resolution is retained in this tab.", { exact: true })).toHaveCount(0);
  expect((await get(workspacePath)).revision).toBe(4);
  await language("zh");
  const translated = page.getByRole("region", { name: "解决合并冲突", exact: true });
  await expect(translated.getByLabel("解决后的文本", { exact: true })).toHaveValue("My final reviewed resolution");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: test.info().outputPath(`conflict-resolution-${width}.png`), fullPage: true });
  await language("en");

  let lostComplete = false;
  const completions: string[] = [];
  await page.route(`**${root}${workspacePath}/complete`, async route => {
    completions.push(route.request().postData()!);
    if (!lostComplete) { const response = await route.fetch(); expect(response.ok()).toBe(true); lostComplete = true; return route.abort("failed"); }
    return route.continue();
  });
  await conflicts.getByRole("button", { name: "Prepare resolved integration", exact: true }).click();
  await expect(conflicts.getByRole("alert")).toContainText("Conflict operation is unconfirmed.");
  await page.reload();
  // Completion stays recoverable by URL if its subsequent report read is unavailable.
  await page.route(`**${root}/integrations/*`, route => route.request().method() === "GET" ? route.abort("failed") : route.continue());
  await conflicts.getByRole("button", { name: "Recover conflict operation", exact: true }).click();
  await expect(conflicts).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Integration review", exact: true }).getByRole("alert")).toBeVisible();
  await page.unroute(`**${root}/integrations/*`);
  await page.reload();
  expect(completions).toHaveLength(2); expect(completions[0]).toBe(completions[1]);
  const digest = new URL(page.url()).searchParams.get("integration")!;
  expect(digest).toBeTruthy();
  await expect(page.getByRole("button", { name: "Merge approved integration", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Approve integration", exact: true }).click();
  await page.getByRole("button", { name: "Merge approved integration", exact: true }).click();
  await expect(page.getByText("Acceptance is complete. Publishing creates a version; it does not deploy to Projects.")).toBeVisible();
  const accepted = await get("/main");
  expect(accepted).toEqual((await get(`/integrations/${encodeURIComponent(digest)}`)).identity.result);
  const version = await post("/versions", { accepted_commit: accepted.commit_id, label: "resolved" });
  await expect.poll(async () => (await get(`/versions/${version.id}`)).state).toBe("published");
  expect(await (await page.request.get(`${root}/versions/${version.id}/content?path=lesson.md`)).text()).toBe("My final reviewed resolution");
  expect(errors).toEqual([]);
});
