import { test, expect } from "@playwright/test";
const origin = "https://127.0.0.1:18443";

for (const width of [390, 1440]) test(`integration operations retain exact requests across reload at ${width}px`, async ({ page }) => {
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
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${branch.id}`);
  const review = page.getByRole("region", { name: "Integration review", exact: true });
  const recover = review.getByRole("button", { name: "Recover integration operation", exact: true });
  const key = `studio-integration:${JSON.stringify([session.issuer, session.subject, library.id, branch.id])}`;
  await expect(review.getByRole("button", { name: "Prepare integration", exact: true })).toBeEnabled();
  await page.evaluate(key => sessionStorage.setItem(key, JSON.stringify({ library: "foreign", branch: "foreign", action: "publish", digest: "", body: "{}" })), key);
  await page.reload();
  await expect(review.getByRole("alert")).toContainText("Local integration recovery is invalid.");
  await expect(review.getByRole("button", { name: "Prepare integration", exact: true })).toBeDisabled();
  await expect(recover).toHaveCount(0);
  await review.getByRole("button", { name: "Discard integration recovery", exact: true }).click();
  await page.getByRole("dialog", { name: "Discard integration recovery", exact: true }).getByRole("button", { name: "Discard", exact: true }).click();
  await expect(review.getByRole("button", { name: "Prepare integration", exact: true })).toBeEnabled();

  const lostOperation = async (suffix: string, trigger: () => Promise<void>, afterLoss?: () => Promise<void>, reload = true) => {
    let lost = false; const bodies: string[] = [];
    await page.route(`**${root}${suffix}`, async route => {
      if (route.request().method() !== "POST") return route.continue();
      bodies.push(route.request().postData()!);
      if (!lost) { const response = await route.fetch(); expect(response.ok(), await response.text()).toBe(true); lost = true; return route.abort("failed"); }
      return route.continue();
    });
    await trigger();
    await expect(review.getByRole("alert").filter({ hasText: "Integration operation is unconfirmed." })).toBeVisible();
    await expect(review.getByRole("button", { name: "Prepare integration", exact: true })).toBeDisabled();
    if (afterLoss) await afterLoss();
    if (reload) await page.reload();
    await expect(recover).toBeEnabled(); await recover.press("Enter");
    await expect(recover).toHaveCount(0);
    expect(bodies).toHaveLength(2); expect(bodies[0]).toBe(bodies[1]);
    await page.unroute(`**${root}${suffix}`);
    return JSON.parse(bodies[0]);
  };
  let newerHead = "";
  const preparation = await lostOperation("/integrations/prepare", () => review.getByRole("button", { name: "Prepare integration", exact: true }).press("Enter"), async () => {
    await post(`/branches/${branch.id}/draft/text`, { expected_head: commit.commit_id, expected_revision: 2, path: "later.md", text: "Later source edit" });
    newerHead = (await post(`/branches/${branch.id}/commits`, { expected_head: commit.commit_id, expected_revision: 3, message: "Later checkpoint" })).commit_id;
  });
  expect(preparation.expected_head).toBe(commit.commit_id);
  const originalDigest = new URL(page.url()).searchParams.get("integration")!;
  expect((await get(`/integrations/${encodeURIComponent(originalDigest)}`)).identity.sources[0].commit_id).toBe(commit.commit_id);
  await expect(review.getByRole("button", { name: "Approve integration", exact: true })).toBeDisabled();
  await review.getByRole("button", { name: "Prepare integration", exact: true }).click();
  await expect(review.getByRole("button", { name: "Approve integration", exact: true })).toBeEnabled();
  const preparedDigest = new URL(page.url()).searchParams.get("integration")!;
  expect(preparedDigest).not.toBe(originalDigest);
  expect((await get(`/integrations/${encodeURIComponent(preparedDigest)}`)).identity.sources[0].commit_id).toBe(newerHead);

  // Even a semantically identical composition has its own exact request body.
  await lostOperation("/integrations/compose", () => review.getByRole("button", { name: "Prepare selected composition", exact: true }).press("Enter"));
  await expect(review.getByRole("button", { name: "Approve integration", exact: true })).toBeEnabled();
  const digest = new URL(page.url()).searchParams.get("integration")!;
  expect(digest).not.toBe(preparedDigest);
  await review.getByLabel("Review rationale", { exact: true }).fill("Exact reviewed rationale");
  const approval = await lostOperation("/integrations/reviews", () => review.getByRole("button", { name: "Approve integration", exact: true }).press("Enter"), async () => {
    await grant("volume.read", "deny", 1);
    await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
    await expect(review).not.toContainText("Exact reviewed rationale");
    await expect(recover).toHaveCount(0);
    expect((await page.request.get(`${root}/integrations/${encodeURIComponent(digest)}`)).status()).toBe(403);
    await grant("volume.read", "allow", 2);
  });
  expect(approval.rationale).toBe("Exact reviewed rationale");
  const reviewed = await get(`/integrations/${encodeURIComponent(digest)}`);
  expect(reviewed.reviews.filter((item: { rationale: string }) => item.rationale === approval.rationale)).toHaveLength(1);

  const merged = await lostOperation("/integrations/merge", () => review.getByRole("button", { name: "Merge approved integration", exact: true }).press("Enter"), async () => {
    expect((await get("/main")).commit_id).not.toBe(main.commit_id);
  });
  expect(merged.expected_main).toBe(main.commit_id);
  const publish = page.getByRole("region", { name: "Publish accepted commit", exact: true });
  await expect(publish).toBeVisible();
  await publish.getByLabel("Version label", { exact: true }).fill("Recovered publication");
  await publish.getByLabel("Release note", { exact: true }).fill("Exact release note");
  const publication = await lostOperation("/versions", () => publish.getByRole("button", { name: "Publish accepted commit", exact: true }).press("Enter"));
  expect(publication.label).toBe("Recovered publication"); expect(publication.note).toBe("Exact release note");
  await expect(publish.getByRole("status")).toContainText("published");
  const versionID = new URL(page.url()).searchParams.get("publication")!; expect(versionID).toBeTruthy();
  expect((await get("/versions")).versions.filter((item: { label: string }) => item.label === publication.label)).toHaveLength(1);
  await page.reload(); await expect(publish.getByRole("status")).toContainText("Recovered publication: published");
  expect((await get(`/versions/${versionID}`)).accepted_commit).toBe((await get("/main")).commit_id);

  // The retained publication identity survives a later metadata outage.
  await page.route(`**${root}/versions/${versionID}`, route => route.abort("failed"));
  await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
  await expect(review.getByRole("alert")).toContainText("Integration evidence is unavailable.");
  await expect(publish).toHaveCount(0);
  expect(new URL(page.url()).searchParams.get("publication")).toBe(versionID);
  await page.unroute(`**${root}/versions/${versionID}`);
  await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
  await expect(publish.getByRole("status")).toContainText("Recovered publication: published");
  // When storage fails, current memory wins over an older persisted record.
  await page.evaluate(key => {
    sessionStorage.setItem(key, JSON.stringify({ library: "stale", branch: "stale" }));
    Object.assign(window, { originalStorageSet: Storage.prototype.setItem, originalStorageRemove: Storage.prototype.removeItem });
    Storage.prototype.setItem = function (key, value) { if (key.startsWith("studio-integration:")) throw new Error("storage unavailable"); return (window as unknown as {originalStorageSet: typeof Storage.prototype.setItem}).originalStorageSet.call(this, key, value); };
    Storage.prototype.removeItem = function (key) { if (key.startsWith("studio-integration:")) throw new Error("storage unavailable"); return (window as unknown as {originalStorageRemove: typeof Storage.prototype.removeItem}).originalStorageRemove.call(this, key); };
  }, key);
  await lostOperation("/integrations/prepare", () => review.getByRole("button", { name: "Prepare integration", exact: true }).press("Enter"), async () => {
    await expect(review.getByText("Local operation recovery is unavailable. Keep this tab open until the operation is confirmed.", { exact: true })).toBeVisible();
    await review.getByRole("button", { name: "Reload integration review", exact: true }).click();
    await expect(review.getByText("Local integration recovery is invalid. Discard it before starting another operation.", { exact: true })).toHaveCount(0);
  }, false);
  await page.evaluate(key => {
    Storage.prototype.setItem = (window as unknown as {originalStorageSet: typeof Storage.prototype.setItem}).originalStorageSet;
    Storage.prototype.removeItem = (window as unknown as {originalStorageRemove: typeof Storage.prototype.removeItem}).originalStorageRemove;
    sessionStorage.removeItem(key);
  }, key);
  await language("zh");
  await expect(page.getByRole("button", { name: "重新加载集成审核", exact: true })).toBeEnabled();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: test.info().outputPath(`integration-recovery-${width}.png`), fullPage: true });
  await language("en");
  expect(errors).toEqual([]);
});
