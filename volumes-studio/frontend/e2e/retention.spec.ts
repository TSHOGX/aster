import { test, expect } from "@playwright/test";
const origin = "https://127.0.0.1:18443";

for (const width of [390, 1440]) test(`version retention review and original lifecycle recovery at ${width}px`, async ({ page }) => {
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
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Retention ${crypto.randomUUID()}` } });
  expect(created.ok()).toBe(true); const library = await created.json();
  const grant = async (action: string, effect: string, revision: number) => {
    const response = await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action, effect, expected_revision: revision } });
    expect(response.ok(), await response.text()).toBe(true);
  };
  // Creating a library grants its creator every Studio action.
  const root = `/volumes-studio/api/v1/libraries/${library.id}`;
  const get = async (suffix: string) => { const response = await page.request.get(root + suffix); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const post = async (suffix: string, data: object) => { const response = await page.request.post(root + suffix, { headers, data: { request_id: crypto.randomUUID(), ...data } }); expect(response.ok(), await response.text()).toBe(true); return response.json(); };
  const main = await get("/main");
  const branch = await post("/branches", { name: "Retained draft", base_commit: main.commit_id });
  const content = `Retained accepted and draft bytes ${crypto.randomUUID()}`;
  await post(`/branches/${branch.id}/draft/text`, { expected_head: branch.head, expected_revision: 1, path: "lesson.md", text: content });
  const commit = await post(`/branches/${branch.id}/commits`, { expected_head: branch.head, expected_revision: 2, message: "Retained content" });
  const prepared = await post("/integrations/prepare", { branch_id: branch.id, expected_head: commit.commit_id, expected_main: main.commit_id, expected_policy_revision: 1 });
  await post("/integrations/reviews", { prepared_digest: prepared.digest, verdict: "approve" });
  const merge = await post("/integrations/merge", { prepared_digest: prepared.digest, expected_main: main.commit_id });
  const version = await post("/versions", { accepted_commit: merge.accepted_commit, label: "Review retention", note: "Private release note" });
  await expect.poll(async () => (await get(`/versions/${version.id}`)).state).toBe("published");
  await page.goto(`/volumes-studio/libraries/${library.id}?view=versions`);
  await page.getByRole("button", { name: "Review retention", exact: true }).press("Enter");
  const detail = page.getByRole("region", { name: "Version retention", exact: true });
  await expect(detail).toContainText("Private release note");
  await expect(detail.locator("dt").filter({ hasText: /^Branch draft references$/ }).locator("+ dd")).toHaveText("1");
  await expect(detail).toContainText("do not block retirement");
  expect(new URL(page.url()).searchParams.get("version_details")).toBe(version.id);
  await page.reload(); await expect(detail).toContainText(merge.accepted_commit);
  const recoveryKey = `studio-version-lifecycle:${JSON.stringify([session.issuer, session.subject, library.id, version.id])}`;
  await page.evaluate(key => sessionStorage.setItem(key, JSON.stringify({ version: "different-version", action: "retire", request_id: crypto.randomUUID() })), recoveryKey);
  await page.reload();
  await expect(detail.getByRole("alert")).toContainText("Local version recovery is invalid.");
  await expect(detail.getByRole("button", { name: "Archive source", exact: true })).toBeDisabled();
  await expect(detail.getByRole("button", { name: "Recover version operation", exact: true })).toHaveCount(0);
  await detail.getByRole("button", { name: "Discard version recovery", exact: true }).click();
  await page.getByRole("dialog", { name: "Discard version recovery", exact: true }).getByRole("button", { name: "Discard", exact: true }).click();
  await expect(detail.getByRole("button", { name: "Archive source", exact: true })).toBeEnabled();

  // An unavailable retention read clears previously displayed details.
  await page.route(`**${root}/versions/${version.id}/retention`, route => route.abort("failed"));
  await detail.getByRole("button", { name: "Reload version details", exact: true }).click();
  await expect(detail.getByRole("alert")).toContainText("Version and retention details are unavailable.");
  await expect(detail.locator("dl")).toHaveCount(0); await expect(detail).not.toContainText("Private release note");
  await page.unroute(`**${root}/versions/${version.id}/retention`);
  await detail.getByRole("button", { name: "Reload version details", exact: true }).click();
  await expect(detail).toContainText("Private release note");

  await language("zh");
  const translated = page.getByRole("region", { name: "版本保留", exact: true });
  await expect(translated).toContainText("分支草稿引用");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: test.info().outputPath(`retention-${width}.png`), fullPage: true });
  await language("en");

  for (const action of ["archive", "retire"]) {
    let lost = false; const bodies: string[] = [];
    await page.route(`**${root}/versions/${version.id}/lifecycle`, async route => {
      bodies.push(route.request().postData()!);
      if (!lost) { const response = await route.fetch(); expect(response.ok()).toBe(true); lost = true; return route.abort("failed"); }
      return route.continue();
    });
    await detail.getByRole("button", { name: action === "archive" ? "Archive source" : "Retire source", exact: true }).press("Enter");
    const confirmation = page.getByRole("dialog", { name: action === "archive" ? "Archive version" : "Retire version", exact: true });
    await expect(confirmation).toContainText("existing imported copies remain");
    await confirmation.getByRole("button", { name: "Continue", exact: true }).press("Enter");
    await expect(detail.getByRole("alert")).toContainText("Version operation is unconfirmed.");
    await page.reload();
    await detail.getByRole("button", { name: "Recover version operation", exact: true }).click();
    await expect(detail.getByRole("button", { name: "Recover version operation", exact: true })).toHaveCount(0);
    await expect(detail).toContainText(action === "archive" ? "archived" : "Source retired.");
    expect(bodies).toHaveLength(2); expect(bodies[0]).toBe(bodies[1]);
    expect(JSON.parse(bodies[0]).action).toBe(action);
    await page.unroute(`**${root}/versions/${version.id}/lifecycle`);
  }
  expect((await get(`/versions/${version.id}`)).state).toBe("retired");
  const retired = await page.request.get(`${root}/versions/${version.id}/content?path=lesson.md`);
  expect(retired.ok()).toBe(false);
  const accepted = await page.request.get(`${root}/commits/${merge.accepted_commit}/content?path=lesson.md`);
  expect(accepted.ok(), await accepted.text()).toBe(true); expect(await accepted.text()).toBe(content);
  const draft = await page.request.get(`${root}/branches/${branch.id}/draft/content?path=lesson.md&revision=2`);
  expect(draft.ok()).toBe(true); expect(await draft.text()).toBe(content);

  await grant("volume.read", "deny", 1);
  // A background refresh may notice the denial first and close the details.
  await detail.getByRole("button", { name: "Reload version details", exact: true }).click({ timeout: 5000 }).catch(() => {});
  await expect(page.getByText("Private release note", { exact: true })).toHaveCount(0);
  expect((await page.request.get(`${root}/versions/${version.id}/retention`)).status()).toBe(403);
  await grant("volume.read", "allow", 2);
  // WebKit reports a fetch cancelled by reload as a page error; let reads settle first.
  await page.waitForLoadState("networkidle"); await page.reload(); await expect(detail).toContainText("Source retired.");
  expect(errors).toEqual([]);
});
