import { test, expect } from "@playwright/test";
test.use({ viewport: process.env.STUDIO_NARROW === "1" ? { width: 390, height: 844 } : { width: 1440, height: 1000 } });
const origin = "https://127.0.0.1:18443";
test("branch creation and selection use real scoped authority", async ({ page }) => {
  const login = await page.request.post(`${origin}/api/auth/login`, { headers: { Origin: origin }, data: { username: "studio-admin", password: "browser-fixture-password" } });
  expect(login.ok()).toBeTruthy();
  await page.goto("/volumes-studio/");
  await page.getByRole("link", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "New library", exact: true })).toBeVisible();
  const session = await (await page.request.get("/volumes-studio/api/v1/session")).json();
  const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Branch browser fixture ${crypto.randomUUID()}` } });
  expect(created.ok(), await created.text()).toBeTruthy();
  const library = await created.json();
  for (const action of ["volume.read", "volume.author", "volume.review", "volume.merge", "volume.publish"]) {
    const grant = await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action, effect: "allow", expected_revision: 0 } });
    expect(grant.ok(), await grant.text()).toBeTruthy();
  }
  await page.goto(`/volumes-studio/libraries/${library.id}`);
  await page.getByLabel("New branch name", { exact: true }).fill("Human edits");
  await page.getByRole("button", { name: "Create branch from main", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Working branch", exact: true })).toHaveValue(/.+/);
  const selected = await page.getByRole("combobox", { name: "Working branch", exact: true }).inputValue();
  await page.reload();
  await expect(page.getByRole("combobox", { name: "Working branch", exact: true })).toHaveValue(selected);
  const showFiles = page.getByRole("button", { name: "Show files", exact: true });
  if (await showFiles.isVisible()) await showFiles.click();
  await page.getByRole("button", { name: "Add files", exact: true }).click();
  await page.getByRole("menuitem", { name: "New file", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill("lesson.md");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const editor = page.getByRole("textbox", { name: "Contents of lesson.md" });
  await expect(editor).toBeVisible();
  await editor.fill("Reviewed lesson from a branch.");
  await page.getByRole("button", { name: "Save file", exact: true }).click();
  await expect(page.getByText("Unsaved editor changes are excluded from this commit.")).toHaveCount(0);
  await page.getByLabel("Commit message", { exact: true }).fill("Add reviewed lesson");
  await page.getByRole("button", { name: "Commit saved draft", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: "Committed saved revision" })).toBeVisible();
  await page.getByRole("button", { name: "Prepare integration", exact: true }).click();
  await expect(page.getByText("Ready for expert review", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "added: lesson.md", exact: true }).click();
  await expect(page.locator("pre").filter({ hasText: "Reviewed lesson from a branch." })).toBeVisible();
  await page.getByLabel("Review rationale", { exact: true }).fill("Verified result");
  await page.getByRole("button", { name: "Approve integration", exact: true }).click();
  await page.getByRole("button", { name: "Merge approved integration", exact: true }).click();
  await expect(page.getByText("Acceptance is complete. Publishing creates a version; it does not deploy to Projects.")).toBeVisible();

  await page.getByLabel("Version label", { exact: true }).fill("reviewed-v1");
  await page.getByLabel("Release note", { exact: true }).fill("Published reviewed integration");
  const publication = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith(`/libraries/${library.id}/versions`));
  await page.getByRole("button", { name: "Publish accepted commit", exact: true }).click();
  const response = await publication;
  expect(response.status()).toBe(202);
  const version = await response.json();
  await expect.poll(async () => (await (await page.request.get(`/volumes-studio/api/v1/libraries/${library.id}/versions/${version.id}`)).json()).state).toBe("published");
  const content = await page.request.get(`/volumes-studio/api/v1/libraries/${library.id}/versions/${version.id}/content?path=lesson.md`);
  expect(content.ok()).toBeTruthy();
  expect(await content.text()).toBe("Reviewed lesson from a branch.");
  expect(version.accepted_commit).toMatch(/^[a-f0-9]{64}$/);

  const originalBranch = await (await page.request.get(`/volumes-studio/api/v1/libraries/${library.id}/branches/${selected}`)).json();
  const post = async (path: string, data: object) => {
    const response = await page.request.post(`/volumes-studio/api/v1/libraries/${library.id}${path}`, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy(); return response.json();
  };
  const competing = await post("/branches", { request_id: crypto.randomUUID(), name: "Competing edits", base_commit: originalBranch.initial_base });
  await post(`/branches/${competing.id}/draft/text`, { request_id: crypto.randomUUID(), expected_revision: 1, expected_head: competing.head, path: "lesson.md", text: "Competing lesson." });
  await post(`/branches/${competing.id}/commits`, { request_id: crypto.randomUUID(), expected_revision: 2, expected_head: competing.head, message: "Competing candidate" });
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${competing.id}`);
  await expect(page.getByRole("combobox", { name: "Working branch", exact: true })).toHaveValue(competing.id);
  await page.getByRole("button", { name: "Prepare integration", exact: true }).click();
  const conflicts = page.getByRole("region", { name: "Resolve merge conflicts", exact: true });
  await expect(conflicts).toBeVisible();
  await conflicts.getByRole("button", { name: "lesson.md", exact: true }).click();
  await expect(conflicts.locator("pre").filter({ hasText: "Competing lesson." })).toBeVisible();
  await conflicts.getByLabel("Resolution choice", { exact: true }).selectOption("text");
  await conflicts.getByLabel("Resolved text", { exact: true }).fill("Resolved and reviewed lesson.");
  await conflicts.getByRole("button", { name: "Save resolution", exact: true }).click();
  await expect(conflicts.getByText("Resolution saved", { exact: false })).toBeVisible();
  await page.reload();
  await expect(conflicts.getByText("Resolution saved", { exact: false })).toBeVisible();
  await conflicts.getByRole("button", { name: "Prepare resolved integration", exact: true }).click();
  await expect(page.getByRole("button", { name: "Merge approved integration", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Approve integration", exact: true }).click();
  await page.getByRole("button", { name: "Merge approved integration", exact: true }).click();
  await expect(page.getByText("Acceptance is complete. Publishing creates a version; it does not deploy to Projects.")).toBeVisible();

});

test("non-admin experts enter with scoped grants and cannot create or merge libraries", async ({ page, browser }) => {
  const login = await page.request.post(`${origin}/api/auth/login`, {headers:{Origin:origin},data:{username:"studio-admin",password:"browser-fixture-password"}});
  expect(login.ok()).toBeTruthy();
  await page.goto("/volumes-studio/");
  await page.getByRole("link",{name:"Sign in",exact:true}).click();
  const session=await (await page.request.get("/volumes-studio/api/v1/session")).json();
  expect(session.can_create_library).toBe(true);
  const headers={Origin:origin,"X-CSRF-Token":session.csrf};
  const created=await page.request.post("/volumes-studio/api/v1/libraries",{headers,data:{request_id:crypto.randomUUID(),name:`Expert review ${crypto.randomUUID()}`}});
  expect(created.ok(),await created.text()).toBeTruthy();
  const library=await created.json();
  const userResponse=await page.request.post("/api/admin/users",{headers:{Origin:origin},data:{request_id:crypto.randomUUID(),username:`expert-${crypto.randomUUID()}`,display_name:"Scoped expert",role:"user",group_ids:[]}});
  expect(userResponse.status(),await userResponse.text()).toBe(201);
  const user=await userResponse.json();
  const grant=async(subject:string,action:string,effect="allow",revision=0)=>{
    const response=await page.request.post("/api/admin/studio-grants",{headers:{Origin:origin},data:{request_id:crypto.randomUUID(),kind:"account",subject_id:subject,library_id:library.id,action,effect,expected_revision:revision}});
    expect(response.ok(),await response.text()).toBeTruthy();
  };
  for(const action of ["volume.read","volume.author"]) await grant(session.subject,action);
  for(const action of ["volume.read","volume.review"]) await grant(user.user.id,action);
  const root=`/volumes-studio/api/v1/libraries/${library.id}`;
  const post=async(path:string,data:object)=>{
    const response=await page.request.post(root+path,{headers,data});
    expect(response.ok(),await response.text()).toBeTruthy();return response.json();
  };
  const main=await (await page.request.get(root+"/main")).json();
  const branch=await post("/branches",{request_id:crypto.randomUUID(),name:"Candidate",base_commit:main.commit_id});
  await post(`/branches/${branch.id}/draft/text`,{request_id:crypto.randomUUID(),expected_revision:1,expected_head:branch.head,path:"lesson.md",text:"Exact candidate for expert review"});
  const checkpoint=await post(`/branches/${branch.id}/commits`,{request_id:crypto.randomUUID(),expected_revision:2,expected_head:branch.head,message:"Candidate"});
  const prepared=await post("/integrations/prepare",{request_id:crypto.randomUUID(),branch_id:branch.id,expected_head:checkpoint.commit_id,expected_main:main.commit_id,expected_policy_revision:1});
  const context=await browser.newContext({baseURL:origin,ignoreHTTPSErrors:true});
  try {
    const expert=await context.newPage();
    expect((await expert.request.post("/api/auth/login",{headers:{Origin:origin},data:{username:user.user.username,password:user.temporary_password}})).ok()).toBeTruthy();
    expect((await expert.request.post("/api/auth/password/setup",{headers:{Origin:origin},data:{new_password:"permanent-expert-fixture-password"}})).ok()).toBeTruthy();
    await expert.goto("/volumes-studio/");
    await expert.getByRole("link",{name:"Sign in",exact:true}).click();
    await expect(expert.getByRole("button",{name:"New library",exact:true})).toBeDisabled();
    const expertSession=await (await expert.request.get("/volumes-studio/api/v1/session")).json();
    expect(expertSession.can_create_library).toBe(false);
    const expertHeaders={Origin:origin,"X-CSRF-Token":expertSession.csrf};
    expect((await expert.request.post("/volumes-studio/api/v1/libraries",{headers:expertHeaders,data:{request_id:crypto.randomUUID(),name:"Forbidden creation"}})).status()).toBe(403);
    const catalog=await (await expert.request.get("/volumes-studio/api/v1/libraries?mode=workbench")).json();
    expect(catalog.total).toBe(1);expect(catalog.items[0].id).toBe(library.id);
    expect((await expert.request.get("/api/admin/users")).status()).toBe(403);
    await expert.goto(`/volumes-studio/libraries/${library.id}?branch=${branch.id}&integration=${encodeURIComponent(prepared.digest)}`);
    await expert.getByRole("button",{name:"added: lesson.md",exact:true}).click();
    await expect(expert.locator("pre").filter({hasText:"Exact candidate for expert review"})).toBeVisible();
    await expert.getByRole("button",{name:"Approve integration",exact:true}).click();
    await expect(expert.getByRole("button",{name:"Merge approved integration",exact:true})).toBeEnabled();
    await expert.getByRole("button",{name:"Merge approved integration",exact:true}).click();
    await expect(expert.getByRole("region",{name:"Integration review",exact:true}).getByRole("alert")).toBeVisible();
    const unchanged=await (await page.request.get(root+"/main")).json();expect(unchanged.commit_id).toBe(main.commit_id);
    await grant(user.user.id,"volume.read","deny",1);
    expect((await expert.request.get("/volumes-studio/api/v1/libraries?mode=workbench")).status()).toBe(401);
  } finally {await context.close();}
});

test("browser composes selected committed paths across branches", async ({ page }) => {
  const login = await page.request.post(`${origin}/api/auth/login`, { headers: { Origin: origin }, data: { username: "studio-admin", password: "browser-fixture-password" } });
  expect(login.ok()).toBeTruthy();
  await page.goto("/volumes-studio/");
  await page.getByRole("link", { name: "Sign in", exact: true }).click();
  const session = await (await page.request.get("/volumes-studio/api/v1/session")).json();
  const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Composition ${crypto.randomUUID()}` } });
  expect(created.ok(), await created.text()).toBeTruthy();
  const library = await created.json();
  for (const action of ["volume.read", "volume.author", "volume.review", "volume.merge"]) {
    const grant = await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action, effect: "allow", expected_revision: 0 } });
    expect(grant.ok(), await grant.text()).toBeTruthy();
  }
  const root = `/volumes-studio/api/v1/libraries/${library.id}`;
  const post = async (path: string, data: object) => {
    const response = await page.request.post(root + path, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const main = await (await page.request.get(root + "/main")).json();
  const first = await post("/branches", { request_id: crypto.randomUUID(), name: "Selected source", base_commit: main.commit_id });
  const second = await post("/branches", { request_id: crypto.randomUUID(), name: "Deferred source", base_commit: main.commit_id });
  await post(`/branches/${first.id}/draft/text`, { request_id: crypto.randomUUID(), expected_revision: 1, expected_head: first.head, path: "keep.txt", text: "defer this path" });
  await post(`/branches/${first.id}/draft/text`, { request_id: crypto.randomUUID(), expected_revision: 2, expected_head: first.head, path: "selected.txt", text: "select this path" });
  const firstCheckpoint = await post(`/branches/${first.id}/commits`, { request_id: crypto.randomUUID(), expected_revision: 3, expected_head: first.head, message: "Selected source" });
  await post(`/branches/${second.id}/draft/text`, { request_id: crypto.randomUUID(), expected_revision: 1, expected_head: second.head, path: "other.txt", text: "other source" });
  const secondCheckpoint = await post(`/branches/${second.id}/commits`, { request_id: crypto.randomUUID(), expected_revision: 2, expected_head: second.head, message: "Deferred source" });
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${first.id}`);
  const composition = page.getByRole("region", { name: "Compose selected changes", exact: true });
  await expect(composition).toBeVisible();
  await expect(composition.getByText("selected.txt", { exact: true })).toBeVisible();
  await expect(composition.getByText("other.txt", { exact: true })).toBeVisible();
  const firstSet = composition.locator("fieldset").filter({ hasText: "Selected source" });
  await firstSet.getByRole("checkbox").nth(0).uncheck();
  const secondSet = composition.locator("fieldset").filter({ hasText: "Deferred source" });
  await secondSet.getByRole("checkbox").check();
  await composition.getByRole("button", { name: "Prepare selected composition", exact: true }).click();
  await expect(composition.getByRole("list", { name: "Composition selection result", exact: true })).toContainText("keep.txt");
  await expect(page.getByText("Ready for expert review", { exact: false })).toBeVisible();
  expect(firstCheckpoint.commit_id).toMatch(/^[a-f0-9]{64}$/);
  expect(secondCheckpoint.commit_id).toMatch(/^[a-f0-9]{64}$/);
});

test("browser explicitly rebinds a stale prepared policy", async ({ page }) => {
  const login = await page.request.post(`${origin}/api/auth/login`, { headers: { Origin: origin }, data: { username: "studio-admin", password: "browser-fixture-password" } });
  expect(login.ok()).toBeTruthy();
  await page.goto("/volumes-studio/");
  await page.getByRole("link", { name: "Sign in", exact: true }).click();
  const session = await (await page.request.get("/volumes-studio/api/v1/session")).json();
  const headers = { Origin: origin, "X-CSRF-Token": session.csrf };
  const created = await page.request.post("/volumes-studio/api/v1/libraries", { headers, data: { request_id: crypto.randomUUID(), name: `Rebind ${crypto.randomUUID()}` } });
  expect(created.ok(), await created.text()).toBeTruthy();
  const library = await created.json();
  for (const action of ["volume.read", "volume.author", "volume.configure", "volume.review"]) {
    const grant = await page.request.post("/api/admin/studio-grants", { headers: { Origin: origin }, data: { request_id: crypto.randomUUID(), kind: "account", subject_id: session.subject, library_id: library.id, action, effect: "allow", expected_revision: 0 } });
    expect(grant.ok(), await grant.text()).toBeTruthy();
  }
  const root = `/volumes-studio/api/v1/libraries/${library.id}`;
  const post = async (path: string, data: object) => {
    const response = await page.request.post(root + path, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const main = await (await page.request.get(root + "/main")).json();
  const branch = await post("/branches", { request_id: crypto.randomUUID(), name: "Policy candidate", base_commit: main.commit_id });
  await post(`/branches/${branch.id}/draft/text`, { request_id: crypto.randomUUID(), expected_revision: 1, expected_head: branch.head, path: "policy.txt", text: "candidate" });
  const checkpoint = await post(`/branches/${branch.id}/commits`, { request_id: crypto.randomUUID(), expected_revision: 2, expected_head: branch.head, message: "Policy candidate" });
  const prepared = await post("/integrations/prepare", { request_id: crypto.randomUUID(), branch_id: branch.id, expected_head: checkpoint.commit_id, expected_main: main.commit_id, expected_policy_revision: 1 });
  const changed = await post("/merge-policy", { request_id: crypto.randomUUID(), expected_revision: 1, minimum_approvals: 2 });
  expect(changed.revision).toBe(2);
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${branch.id}&integration=${encodeURIComponent(prepared.digest)}`);
  await expect(page.getByRole("button", { name: "Rebind current policy", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Rebind current policy", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Rebind current policy", exact: true }).click();
  await expect.poll(() => new URL(page.url()).searchParams.get("integration")).not.toBe(prepared.digest);
  const rebound = new URL(page.url()).searchParams.get("integration");
  expect(rebound).toBeTruthy();
  expect(rebound).not.toBe(prepared.digest);
  const reboundView = await (await page.request.get(`${root}/integrations/${encodeURIComponent(rebound!)}`)).json();
  expect(reboundView.identity.policy_revision).toBe(2);
});
