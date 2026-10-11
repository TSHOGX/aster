import { expect, test } from "@playwright/test";

test("real owner authenticates both builds and preserves administrative request identity", async ({ page, browser, baseURL }) => {
  await page.goto("/admin/users");
  await page.waitForURL(url => url.pathname === "/" && url.searchParams.get("return_to") === "/admin/users");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await expect(page.getByRole("heading", { name: "Users", exact: true })).toBeVisible();
  let loseReply = true;
  await page.route("**/api/admin/users", async route => {
    if (route.request().method() === "POST" && loseReply) {
      loseReply = false;
      const response = await route.fetch(); expect(response.status()).toBe(201);
      await response.dispose(); await route.abort("failed"); return;
    }
    await route.continue();
  });
  await page.getByRole("link", { name: "Create user", exact: true }).click();
  await page.getByLabel("Username", { exact: true }).fill("browser-user");
  await page.getByLabel("Display name", { exact: true }).fill("Browser user");
  await page.getByRole("combobox", { name: "Membership", exact: true }).selectOption("none");
  await page.getByRole("button", { name: "Create account", exact: true }).click();
  await page.getByRole("button", { name: "Check original request", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("password cannot be shown again");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const listing = await (await page.request.get("/api/admin/users?search=browser-user")).json();
  expect(listing.users).toHaveLength(1); expect(listing.users[0].groups).toEqual([]);
  const userID = listing.users[0].id as string;
  expect(userID).not.toBe("browser-user");
  await page.getByRole("link", { name: "Open this account", exact: true }).click();
  await page.waitForURL(url => url.pathname === `/admin/users/${userID}`);
  await page.getByLabel("Display name", { exact: true }).fill("Browser user edited");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Browser user edited");
  await page.getByRole("button", { name: "Reset password", exact: true }).click();
  const dialog = page.getByRole("dialog"); await expect(dialog).toBeVisible();
  const temporary = await dialog.getByLabel("Temporary password", { exact: true }).inputValue();
  expect(temporary).toHaveLength(24);
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await page.getByRole("link", { name: "View account audit", exact: true }).click();
  await expect(page.getByText("account.password.reset", { exact: true })).toBeVisible();
  const audit = await (await page.request.get(`/api/admin/audit-events?target=${userID}`)).json();
  expect(JSON.stringify(audit)).not.toContain(temporary); expect(JSON.stringify(audit)).not.toContain("password_hash");
  const identity = await (await page.request.get("/api/auth/session")).json();
  const demote = await page.request.patch(`/api/admin/users/${identity.id}`, { data: { request_id: crypto.randomUUID(), expected_revision: identity.revision, role: "user" } });
  expect(demote.status()).toBe(409); expect((await demote.json()).code).toBe("last_admin");
  const project = await page.request.post("/api/projects", { data: { id: "admin-owned-project", title: "Private project" } }); expect(project.status()).toBe(201);
  const ordinaryContext = await browser.newContext({ baseURL, locale: "en-US" });
  try {
    const ordinary = await ordinaryContext.newPage();
    await ordinary.goto("/admin");
    await ordinary.getByLabel("Username", { exact: true }).fill("browser-user");
    await ordinary.getByLabel("Password", { exact: true }).fill(temporary);
    await ordinary.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(ordinary.getByRole("heading", { name: "Set your password", exact: true })).toBeVisible();
    expect((await ordinary.request.get("/api/admin/users")).status()).toBe(401);
    expect((await ordinary.request.get("/api/projects")).status()).toBe(401);
    await ordinary.getByLabel("New password", { exact: true }).fill("browser-user-permanent-password");
    await ordinary.getByRole("button", { name: "Save password", exact: true }).click();
    await expect(ordinary.getByRole("heading", { name: "Access denied", exact: true })).toBeVisible();
    expect((await ordinary.request.get("/api/admin/users")).status()).toBe(403);
    expect((await ordinary.request.get("/api/projects/admin-owned-project")).status()).toBe(404);
    await ordinary.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(ordinary.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
  } finally { await ordinaryContext.close(); }
});

test("real group and policy UI resolves provenance and fences deletion impact", async({page})=>{
  await page.goto("/admin/groups");
  await page.getByLabel("Username",{exact:true}).fill("administrator");
  await page.getByLabel("Password",{exact:true}).fill("web-test-permanent-password");
  await page.getByRole("button",{name:"Sign in",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Groups",exact:true})).toBeVisible();
  const created=await page.request.post("/api/admin/users",{data:{request_id:crypto.randomUUID(),username:"policy-reader",display_name:"Policy Reader",role:"user",group_ids:[]}});
  expect(created.status()).toBe(201);const account=(await created.json()).user;
  await page.getByRole("link",{name:"Policies",exact:true}).click();
  await page.getByRole("link",{name:"Create policy",exact:true}).click();
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Read without export");
  await page.getByRole("combobox",{name:"files.read",exact:true}).selectOption("allow");
  await page.getByRole("combobox",{name:"files.export",exact:true}).selectOption("deny");
  let lost=false;
  await page.route("**/api/admin/policies",async route=>{
    if(route.request().method()==="POST"&&!lost){lost=true;const response=await route.fetch();expect(response.ok()).toBe(true);await response.dispose();await route.abort("failed");return;}
    await route.continue();
  });
  await page.getByRole("button",{name:"Save policy",exact:true}).click();
  await page.getByRole("button",{name:"Check original request",exact:true}).click();
  await expect(page.getByRole("status")).toHaveText("Original change confirmed.");
  const policies=(await (await page.request.get("/api/admin/policies?limit=100")).json()).policies;
  const matches=policies.filter((p:{name:string})=>p.name==="Read without export");expect(matches).toHaveLength(1);let policy=matches[0];
  await page.getByRole("link",{name:"Groups",exact:true}).click();
  await page.getByRole("link",{name:"Create group",exact:true}).click();
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Read reviewers");
  await page.getByRole("button",{name:"Save group",exact:true}).click();
  await expect(page.getByRole("heading",{level:1})).toHaveText("Read reviewers");
  await page.getByLabel("Read without export",{exact:true}).check();
  await page.getByRole("button",{name:"Save policy bindings",exact:true}).click();
  await page.getByRole("combobox",{name:"Account to add",exact:true}).selectOption(account.id);
  await page.getByRole("button",{name:"Add member",exact:true}).click();
  await expect(page.getByRole("button",{name:"Remove policy-reader",exact:true})).toBeVisible();
  const groups=(await(await page.request.get("/api/admin/groups?limit=100")).json()).groups;
  const group=groups.find((g:{name:string})=>g.name==="Read reviewers");
  await page.getByRole("link",{name:"Policy Reader",exact:true}).click();
  const access=page.getByRole("region",{name:"Effective access"});
  await expect(access.getByRole("row").filter({has:page.getByRole("cell",{name:"files.read",exact:true})})).toContainText("Allowed");
  await expect(access.getByRole("row").filter({has:page.getByRole("cell",{name:"files.export",exact:true})})).toContainText("Denied");
  await page.goto(`/admin/groups/${group.id}`);
  await page.getByRole("button",{name:"Preview deletion",exact:true}).click();
  await expect(page.locator(".impact-card")).toContainText("files.read");
  // A bound policy revision changes the impact fingerprint independently of the
  // group's revision. Confirming the old preview must fail in the real store.
  policy=(await(await page.request.get(`/api/admin/policies/${policy.id}`)).json());
  expect((await page.request.patch(`/api/admin/policies/${policy.id}`,{data:{request_id:crypto.randomUUID(),expected_revision:policy.revision,description:"Changed after preview"}})).ok()).toBe(true);
  await page.getByRole("checkbox",{name:/I reviewed the impact/}).check();
  await page.getByRole("button",{name:"Delete group",exact:true}).click();
  await expect(page.getByRole("alert").filter({hasText:"Preview deletion again"})).toBeVisible();
  await page.getByRole("button",{name:"Preview deletion",exact:true}).click();
  await page.getByRole("checkbox",{name:/I reviewed the impact/}).check();
  await page.getByRole("button",{name:"Delete group",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Group deleted",exact:true})).toBeVisible();
  const current=await(await page.request.get(`/api/admin/users/${account.id}/effective-access`)).json();expect(current.actions["files.read"].allowed).toBe(false);
  const user=await(await page.request.get(`/api/admin/users/${account.id}`)).json();expect(user.groups).toEqual([]);
  const audit=await(await page.request.get(`/api/admin/audit-events?target=${group.id}`)).json();expect(audit.events.some((e:{action:string})=>e.action==="group.delete")).toBe(true);
});

test("private inspection requires an explicit grant and records its purpose",async({page})=>{
  await page.goto("/admin/users");await page.getByLabel("Username",{exact:true}).fill("administrator");await page.getByLabel("Password",{exact:true}).fill("web-test-permanent-password");await page.getByRole("button",{name:"Sign in",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Users",exact:true})).toBeVisible();
  const actor=await(await page.request.get("/api/auth/session")).json();
  expect((await page.request.post("/api/projects",{data:{id:"inspection-project",title:"Inspection project"}})).ok()).toBe(true);
  expect((await page.request.post("/api/projects/inspection-project/sessions",{data:{id:"inspection-session",title:"Inspection conversation"}})).ok()).toBe(true);
  const path=`/api/admin/users/${actor.id}/conversations/inspection-session/inspect`;
  expect((await page.request.post(path,{data:{purpose:"Case 7",after_sequence:0,limit:20}})).status()).toBe(403);
  await page.goto(`/admin/users/${actor.id}`);
  const panel=page.getByRole("region",{name:"Private conversation inspection"});
  await expect(panel).toContainText("An explicit admin.content.read grant is required");await expect(panel.getByRole("button",{name:"Inspect conversation"})).toHaveCount(0);
  const policy=await(await page.request.post("/api/admin/policies",{data:{request_id:crypto.randomUUID(),name:"Content inspection",statements:{"admin.content.read":"allow"}}})).json();
  const group=await(await page.request.post("/api/admin/groups",{data:{request_id:crypto.randomUUID(),name:"Content inspectors"}})).json();
  const binding=await(await page.request.put(`/api/admin/groups/${group.group.id}/policies`,{data:{request_id:crypto.randomUUID(),expected_revision:group.group.revision,policy_ids:[policy.policy.id]}})).json();
  expect((await page.request.patch(`/api/admin/groups/${group.group.id}/members`,{data:{request_id:crypto.randomUUID(),expected_revision:binding.group.revision,add_user_ids:[actor.id],remove_user_ids:[]}})).ok()).toBe(true);
  await page.reload();
  await panel.getByRole("textbox",{name:"Conversation ID",exact:true}).fill("inspection-session");
  await panel.getByRole("textbox",{name:"Inspection purpose",exact:true}).fill("Case 7: verify conversation state");
  await panel.getByRole("button",{name:"Inspect conversation",exact:true}).click();
  await expect(panel.getByRole("heading",{name:"Inspection conversation",exact:true})).toBeVisible();
  const audit=await(await page.request.get("/api/admin/audit-events?action=admin.content.read&target=inspection-session")).json();
  expect(audit.events).toHaveLength(1);expect(audit.events[0].details.purpose).toBe("Case 7: verify conversation state");expect(audit.events[0].details.message_count).toBe(0);
  await panel.getByRole("button",{name:"Clear inspected content",exact:true}).click();await expect(panel.getByRole("heading",{name:"Inspection conversation",exact:true})).toHaveCount(0);
});

test("real network editors preserve overrides, stale edits and lost-reply identity", async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto("/admin/network");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Default network settings" })).toBeVisible();
  const identity = await (await page.request.get("/api/auth/session")).json();
  const before = await (await page.request.get("/api/admin/network/default")).json();
  await page.getByRole("button", { name: "Add resolver", exact: true }).click();
  await page.getByLabel(`Resolver ${before.resolvers.length + 1}`, { exact: true }).fill("192.0.2.53");
  await page.getByRole("button", { name: "Save network settings" }).click();
  await expect(page.getByRole("status")).toContainText("Saved.");
  const path = `/api/admin/users/${identity.id}/network`;
  await page.goto(`/admin/users/${identity.id}/network`);
  await expect(page.getByRole("combobox", { name: "Network policy" })).toHaveValue("inherit");
  await expect(page.getByRole("button", { name: "Add resolver", exact: true })).toBeDisabled();
  await page.getByRole("combobox", { name: "Network policy" }).selectOption("override");
  await page.getByLabel("Resolver 1", { exact: true }).fill("198.51.100.53");
  let lost = false;
  const requests: unknown[] = [];
  await page.route(`**${path}`, async route => {
    if (route.request().method() === "PATCH") {
      requests.push(route.request().postDataJSON());
      if (!lost) { lost = true; const response = await route.fetch(); expect(response.ok()).toBe(true); await response.dispose(); await route.abort("failed"); return; }
    }
    await route.continue();
  });
  await page.getByRole("button", { name: "Save network settings" }).click();
  await page.getByRole("button", { name: "Check original request" }).click();
  await expect(page.getByRole("status")).toContainText("Original change confirmed");
  expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
  await page.getByLabel("Resolver 1", { exact: true }).fill("203.0.113.53");
  const current = await (await page.request.get("/api/admin/network/default")).json();
  expect((await page.request.patch("/api/admin/network/default", { data: { request_id: crypto.randomUUID(), expected_revision: current.configuration_revision, direct: [], resolvers: ["192.0.2.54"] } })).ok()).toBe(true);
  await page.getByRole("button", { name: "Save network settings" }).click();
  await expect(page.locator("main").getByRole("alert")).toContainText("Settings changed");
  await page.getByRole("button", { name: "Reload current settings" }).click();
  await expect(page.getByRole("status")).toContainText("Current settings loaded");
  await expect(page.getByLabel("Resolver 1", { exact: true })).toHaveValue("203.0.113.53");
  await page.getByRole("button", { name: "Save network settings" }).click();
  await expect(page.getByRole("status")).toContainText("Saved.");
  await page.getByRole("combobox", { name: "Network policy" }).selectOption("inherit");
  await expect(page.getByLabel("Resolver 1", { exact: true })).toHaveValue("192.0.2.54");
  page.once("dialog", dialog => dialog.dismiss());
  await page.getByRole("link", { name: "Back to account", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/users/${identity.id}/network$`));
  await page.getByRole("button", { name: "Save network settings" }).click();
  await expect(page.getByRole("status")).toContainText("Saved.");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("user-network-mobile.png") });
  const saved = await (await page.request.get(path)).json();
  expect(saved.mode).toBe("inherit"); expect(saved.override).toBeNull(); expect(saved.desired.resolvers).toEqual(["192.0.2.54"]);
  const audit = await (await page.request.get(`/api/admin/audit-events?target=${identity.id}`)).json();
  expect(JSON.stringify(audit)).toContain("network.user.update");
});

test("real resource editors preserve exact quantities and show retained usage to the owner", async ({ page, browser, baseURL }, info) => {
  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto("/admin/groups");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Groups", exact: true })).toBeVisible();
  const groupResult = await page.request.post("/api/admin/groups", { data: { request_id: crypto.randomUUID(), name: "Resource grants", description: "Browser resource test" } });
  expect(groupResult.status()).toBe(200); const group = (await groupResult.json()).group;
  const created = await page.request.post("/api/admin/users", { data: { request_id: crypto.randomUUID(), username: "resource-reader", display_name: "Resource Reader", role: "user", group_ids: ["standard-users", group.id] } });
  expect(created.status()).toBe(201); const result = await created.json(), user = result.user;
  const ownerContext = await browser.newContext({ baseURL, locale: "en-US", viewport: { width: 390, height: 900 } });
  try {
    const owner = await ownerContext.newPage();
    await owner.goto("/");
    await owner.getByLabel("Username", { exact: true }).fill(user.username);
    await owner.getByLabel("Password", { exact: true }).fill(result.temporary_password);
    await owner.getByRole("button", { name: "Sign in", exact: true }).click();
    await owner.getByLabel("New password", { exact: true }).fill("resource-reader-permanent-password");
    await owner.getByRole("button", { name: "Save password", exact: true }).click();
    await expect(owner.getByRole("button", { name: "Open navigation", exact: true })).toBeVisible();
    for (let i = 0; i < 4; i++) expect((await owner.request.post("/api/projects", { data: { id: `resource-project-${i}`, title: `Resource ${i}` } })).status()).toBe(201);
    await page.goto(`/admin/groups/${group.id}/resources`);
    await page.getByLabel("Persistent storage mode", { exact: true }).selectOption("explicit");
    await page.getByLabel("Persistent storage default", { exact: true }).fill("9007199254740993");
    await page.getByLabel("Persistent storage ceiling", { exact: true }).fill("9007199254740994");
    await page.getByRole("button", { name: "Preview resource changes", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Change preview", exact: true })).toBeVisible();
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Save resource settings", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Resource settings saved");
    await expect(page.getByLabel("Persistent storage default", { exact: true })).toHaveValue("9007199254740993");
    const policy = await (await page.request.get(`/api/admin/groups/${group.id}/resource-policy`)).json();
    expect(policy.dimensions.persistent_bytes).toEqual({ default: "9007199254740993", ceiling: "9007199254740994" });
    const userPath = `/api/admin/users/${user.id}/resource-settings`;
    await page.goto(`/admin/users/${user.id}/resources`);
    await page.getByLabel("Projects mode", { exact: true }).selectOption("explicit");
    await page.getByLabel("Projects requested", { exact: true }).fill("2");
    await page.getByRole("button", { name: "Preview resource changes", exact: true }).click();
    await expect(page.getByText("Usage overages: 0 → 1", { exact: true })).toBeVisible();
    const changedGroup = await (await page.request.get(`/api/admin/groups/${group.id}`)).json();
    expect((await page.request.patch(`/api/admin/groups/${group.id}`, { data: { request_id: crypto.randomUUID(), expected_revision: changedGroup.revision, description: "Changed after preview" } })).ok()).toBe(true);
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Save resource settings", exact: true }).click();
    await expect(page.locator("main").getByRole("alert")).toContainText("Settings or dependencies changed");
    await page.getByRole("button", { name: "Reload current settings", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Your edits are retained");
    await expect(page.getByLabel("Projects requested", { exact: true })).toHaveValue("2");
    await page.getByRole("button", { name: "Preview resource changes", exact: true }).click();
    await expect(page.getByText("Usage overages: 0 → 1", { exact: true })).toBeVisible();
    let lost = false;
    const requests: unknown[] = [];
    await page.route(`**${userPath}`, async route => {
      if (route.request().method() === "PATCH") {
        requests.push(route.request().postDataJSON());
        if (!lost) { lost = true; const response = await route.fetch(); expect(response.ok()).toBe(true); await response.dispose(); await route.abort("failed"); return; }
      }
      await route.continue();
    });
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Save resource settings", exact: true }).click();
    await page.getByRole("button", { name: "Check original request", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Original resource change confirmed");
    expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
    const usage = await (await owner.request.get("/api/me/usage")).json();
    expect(usage.settings.effective.projects.effective).toBe("2"); expect(usage.usage.projects.used).toBe("4");
    expect((await owner.request.patch(userPath, { data: requests[0] })).status()).toBe(403);
    await owner.reload();
    await owner.getByRole("button", { name: "Open navigation", exact: true }).click();
    await owner.getByRole("button", { name: "Account", exact: true }).click();
    await owner.getByRole("menuitem", { name: "Settings", exact: true }).click();
    const dialog = owner.getByRole("dialog", { name: "Settings", exact: true });
    await dialog.getByText("Resources and usage", { exact: true }).click();
    await expect(dialog.getByText("Projects: 2 count", { exact: true })).toBeVisible();
    await dialog.getByText("Projects: 2 count", { exact: true }).click();
    await expect(dialog.locator(".resource-allowance[open]").getByText("Requested override", { exact: true })).toBeVisible();
    await dialog.getByText("Usage overages (1)", { exact: true }).click();
    await expect(dialog.getByText("Projects: Requested 4; allowed 2", { exact: true })).toBeVisible();
    expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await owner.screenshot({ path: info.outputPath("own-resource-usage-mobile.png") });
    await page.screenshot({ path: info.outputPath("admin-resource-usage-mobile.png") });
  } finally { await ownerContext.close(); }
});
