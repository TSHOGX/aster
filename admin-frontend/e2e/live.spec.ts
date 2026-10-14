import { chooseValue } from "./picker";
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
  await chooseValue(page.getByRole("combobox", { name: "Membership", exact: true }), "none");
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
  await chooseValue(page.getByRole("combobox",{name:"files.read",exact:true}), "allow");
  await chooseValue(page.getByRole("combobox",{name:"files.export",exact:true}), "deny");
  let lost=false;
  await page.route("**/api/admin/policies",async route=>{
    if(route.request().method()==="POST"&&!lost){lost=true;const response=await route.fetch();expect(response.ok()).toBe(true);await response.dispose();await route.abort("failed");return;}
    await route.continue();
  });
  await page.getByRole("button",{name:"Save policy",exact:true}).click();
  await page.getByRole("button",{name:"Check original request",exact:true}).click();
  await expect(page.getByRole("status").filter({ hasText: "Original change confirmed." })).toHaveText("Original change confirmed.");
  const policies=(await (await page.request.get("/api/admin/policies?limit=100")).json()).policies;
  const matches=policies.filter((p:{name:string})=>p.name==="Read without export");expect(matches).toHaveLength(1);let policy=matches[0];
  await page.getByRole("link",{name:"Groups",exact:true}).click();
  await page.getByRole("link",{name:"Create group",exact:true}).click();
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Read reviewers");
  await page.getByRole("button",{name:"Save group",exact:true}).click();
  await expect(page.getByRole("heading",{level:1})).toHaveText("Read reviewers");
  await page.getByLabel("Read without export",{exact:true}).check();
  await page.getByRole("button",{name:"Save policy bindings",exact:true}).click();
  await chooseValue(page.getByRole("combobox",{name:"Account to add",exact:true}), account.id);
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

test("real network templates preserve lost-reply identity and account defaults", async ({ page },info) => {
 await page.setViewportSize({width:390,height:900});await page.goto("/admin/resources/network");
 await page.getByLabel("Username",{exact:true}).fill("administrator");await page.getByLabel("Password",{exact:true}).fill("web-test-permanent-password");await page.getByRole("button",{name:"Sign in",exact:true}).click();
 const card=page.locator("article").filter({has:page.getByRole("heading",{name:"Restricted",exact:true})});await card.getByRole("button",{name:"Copy and customize",exact:true}).click();
 await page.getByLabel("DNS resolvers",{exact:true}).fill("192.0.2.53");
 await page.getByRole("button",{name:"Validate and review",exact:true}).click();
 let lost=false;const requests:unknown[]=[];
 await page.route("**/api/admin/resource-templates/network",async route=>{
  const body=route.request().postDataJSON();if(route.request().method()==="POST"&&!body.preview){requests.push(body);if(!lost){lost=true;const response=await route.fetch();expect(response.ok()).toBe(true);await response.dispose();await route.abort("failed");return;}}await route.continue();
 });
 await page.getByRole("button",{name:"Confirm and save",exact:true}).click();await page.getByRole("button",{name:"Check original request",exact:true}).click();await expect(page.getByRole("button",{name:"Create template",exact:true})).toBeVisible();expect(requests).toHaveLength(2);expect(requests[0]).toEqual(requests[1]);
 const templates=(await(await page.request.get("/api/admin/resource-templates/network")).json()).templates;const customized=templates.find((t:{name:string})=>t.name==="Restricted copy");expect(customized.builtin).toBe(false);expect(customized.network.resolvers).toEqual(["192.0.2.53"]);
 const policyResponse=await page.request.post("/api/admin/policies",{data:{request_id:crypto.randomUUID(),name:"Customized network access",resources:[{kind:"network_template",id:customized.id,access:"read_only"}]}});
 expect(policyResponse.ok()).toBe(true);const {policy}=await policyResponse.json();
 const group=await(await page.request.get("/api/admin/groups/standard-users")).json();
 const binding=await page.request.put("/api/admin/groups/standard-users/policies",{data:{request_id:crypto.randomUUID(),expected_revision:group.revision,policy_ids:[...group.policy_ids,policy.id]}});expect(binding.ok()).toBe(true);
 const identity=await(await page.request.get("/api/auth/session")).json();await page.goto(`/admin/users/${identity.id}/configuration`);await chooseValue(page.getByRole("combobox",{name:"Network default",exact:true}), customized.id);await page.getByRole("button",{name:"Review administrator defaults",exact:true}).click();await page.getByRole("button",{name:"Confirm and save",exact:true}).click();await expect(page.getByRole("button",{name:"Review administrator defaults",exact:true})).toBeDisabled();await page.reload();await expect(page.getByRole("combobox",{name:"Network default",exact:true})).toHaveAttribute("data-value", customized.id);
 await page.screenshot({path:info.outputPath("configuration-defaults-mobile.png")});
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
    await chooseValue(page.getByRole("combobox", { name:"Persistent storage mode", exact: true }), "explicit");
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
    await chooseValue(page.getByRole("combobox", { name:"Projects mode", exact: true }), "explicit");
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

test("real environment templates validate mount paths and preserve reviewed saves", async ({page},info)=>{
 await page.setViewportSize({width:390,height:900});await page.goto("/admin/resources/environment");await page.getByLabel("Username",{exact:true}).fill("administrator");await page.getByLabel("Password",{exact:true}).fill("web-test-permanent-password");await page.getByRole("button",{name:"Sign in",exact:true}).click();
 await page.locator("article").filter({has:page.getByRole("heading",{name:"Minimal Agent",exact:true})}).getByRole("button",{name:"Edit",exact:true}).click();
 await page.getByLabel("Mount path",{exact:true}).first().fill("/etc/blocked");await page.getByRole("button",{name:"Validate and review",exact:true}).click();await expect(page.locator("main").getByRole("alert")).toBeVisible();
 await page.getByLabel("Mount path",{exact:true}).first().fill("/workspaces/browser");await page.getByRole("button",{name:"Validate and review",exact:true}).click();await page.getByRole("button",{name:"Confirm and save",exact:true}).click();await expect(page.getByRole("region",{name:"Environment updates",exact:true})).toBeVisible();await expect(page.getByText("No updates published.",{exact:false})).toBeVisible();
 const templates=(await(await page.request.get("/api/admin/resource-templates/environment")).json()).templates;expect(templates.find((t:{id:string})=>t.id==="standard-agent").environment.mounts[0].destination).toBe("/workspaces/browser");
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);await page.screenshot({path:info.outputPath("environment-templates-mobile.png")});
});

test("real Project administration is metadata-only and leaves an unbound Project cold", async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto("/admin/projects");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Projects and Volumes", exact: true })).toBeVisible();
  const response = await page.request.post("/api/projects", { data: { id: "metadata-browser", title: "Metadata browser Project" } }); expect(response.status()).toBe(201);
  const identity = await (await page.request.get("/api/auth/session")).json();
  await page.getByLabel("Owner account ID", { exact: true }).fill(identity.id);
  await page.getByLabel("Search Projects", { exact: true }).fill("Metadata browser Project");
  await page.getByRole("button", { name: "Filter Projects", exact: true }).click();
  await page.getByRole("link", { name: "Metadata browser Project", exact: true }).click();
  await expect(page.getByRole("heading", { name: "No saved environment", exact: true })).toBeVisible();
  await expect(page.getByText("No lifecycle operation has been recorded.", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Refresh observations", exact: true }).click();
  await expect(page.getByRole("button", { name: "Refresh observations", exact: true })).toBeEnabled();
  const detail = await (await page.request.get("/api/admin/projects/metadata-browser")).json();
  expect(detail.binding).toBeNull(); expect(detail.bound).toBe(false); expect(detail.state).toBe("created"); expect(detail.operation).toBeNull(); expect(detail.allocated_bytes).toBe("0"); expect(detail.reserved_bytes).toBe("0");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("project-metadata-mobile.png") });
  await page.getByRole("link", { name: "Back to Projects", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Projects and Volumes", exact: true })).toBeVisible();
});

test("suspension and enable preserve separate sharing fences until explicit owner reactivation", async ({ page, browser, baseURL }) => {
  await page.goto("/admin/users");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Users", exact: true })).toBeVisible();
  const admin = await (await page.request.get("/api/auth/session")).json();
  const policy = await (await page.request.post("/api/admin/policies", { data: { request_id: crypto.randomUUID(), name: "Suspension public publisher", statements: { "shares.public.create": "allow" } } })).json();
  const group = await (await page.request.post("/api/admin/groups", { data: { request_id: crypto.randomUUID(), name: "Suspension publishers" } })).json();
  expect((await page.request.put(`/api/admin/groups/${group.group.id}/policies`, { data: { request_id: crypto.randomUUID(), expected_revision: group.group.revision, policy_ids: [policy.policy.id] } })).ok()).toBe(true);
  const created = await (await page.request.post("/api/admin/users", { data: { request_id: crypto.randomUUID(), username: "suspension-owner", display_name: "Suspension Owner", role: "user", group_ids: ["standard-users", group.group.id] } })).json();
  const context = await browser.newContext({ baseURL, locale: "en-US" });
  try {
    const owner = await context.newPage();
    expect((await owner.request.post("/api/auth/login", { data: { username: "suspension-owner", password: created.temporary_password } })).ok()).toBe(true);
    expect((await owner.request.post("/api/auth/password/setup", { data: { new_password: "suspension-owner-password" } })).ok()).toBe(true);
    expect((await owner.request.post("/api/projects", { data: { id: "suspension-share", title: "Retained sharing" } })).status()).toBe(201);
    const session = "/api/projects/suspension-share/sessions/suspension-conversation";
    expect((await owner.request.post("/api/projects/suspension-share/sessions", { data: { id: "suspension-conversation", title: "Retained conversation" } })).ok()).toBe(true);
    expect((await owner.request.post(`${session}/readers`, { data: { reader_id: admin.id } })).ok()).toBe(true);
    const shareResponse = await owner.request.post(`${session}/shares`, { data: { paths: [] } }); expect(shareResponse.status()).toBe(201);
    const share = await shareResponse.json();
    const publicPath = `/api/shared/${share.token}`;
    const publicBefore = await owner.request.get(publicPath);
    expect(publicBefore.ok()).toBe(true);
    await page.goto(`/admin/users/${created.user.id}`);
    await expect(page.getByRole("button", {name:"Suspend account",exact:true})).toBeVisible();
    const suspensionResponse=page.waitForResponse(response=>response.url().endsWith(`/users/${created.user.id}/suspend`) && response.request().method()==="POST");
    page.once("dialog",dialog=>dialog.accept());
    await page.getByRole("button",{name:"Suspend account",exact:true}).click();
    const suspendedResponse=await suspensionResponse;expect(suspendedResponse.status()).toBe(202);
    await page.getByRole("link",{name:"View suspension operation",exact:true}).click();
    await expect(page.getByRole("link",{name:"suspension-share",exact:true})).toBeVisible();
    await expect(page.getByText("The request is recorded. Resource work has not been confirmed complete.",{exact:true})).toBeVisible();
    expect((await owner.request.get(publicPath)).status()).toBe(404);
    expect((await owner.request.get("/api/auth/session")).status()).toBe(401);
    await page.goto(`/admin/users/${created.user.id}`);
    await page.getByRole("button",{name:"Enable account",exact:true}).click();
    await expect(page.getByRole("status")).toContainText("Account enabled. Projects, schedules and sharing remain suspended");
    expect((await owner.request.get(publicPath)).status()).toBe(404);
    expect((await owner.request.post("/api/auth/login", { data: { username: "suspension-owner", password: "suspension-owner-password" } })).ok()).toBe(true);
    await owner.goto("/?view=share&project=suspension-share&session=suspension-conversation");
    await expect(owner.getByRole("button", { name: "Reactivate reader", exact: true })).toBeVisible();
    await expect(owner.getByRole("button", { name: "Reactivate public link", exact: true })).toBeVisible();
    await owner.getByRole("button", { name: "Reactivate reader", exact: true }).click();
    await expect(owner.getByRole("button", { name: "Reactivate reader", exact: true })).toHaveCount(0);
    await owner.getByRole("button", { name: "Reactivate public link", exact: true }).click();
    await expect(owner.getByRole("button", { name: "Reactivate public link", exact: true })).toHaveCount(0);
    expect((await owner.request.get(publicPath)).ok()).toBe(true);
    const project = await (await page.request.get("/api/admin/projects/suspension-share")).json();
    expect(project.bound).toBe(false); expect(project.state).toBe("created");
    const audit = await (await page.request.get(`/api/admin/audit-events?actor=${created.user.id}`)).json();
    expect(JSON.stringify(audit)).toContain("share.reactivate"); expect(JSON.stringify(audit)).toContain("conversation.reactivate");
  } finally { await context.close(); }
});

test("admin Project controls require interruption review and retry the same accepted operation", async ({ page }) => {
  await page.goto("/admin/projects");
  await page.getByLabel("Username", { exact: true }).fill("administrator");
  await page.getByLabel("Password", { exact: true }).fill("web-test-permanent-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Projects and Volumes", exact: true })).toBeVisible();
  expect((await page.request.post("/api/projects", { data: { id: "admin-controls-browser", title: "Control review" } })).status()).toBe(201);
  await page.goto("/admin/projects/admin-controls-browser");
  const controls = page.getByRole("region", { name: "Administrative runtime controls", exact: true });
  await expect(controls.getByRole("button", { name: "Pause Project", exact: true })).toBeDisabled();
  await expect(controls.getByRole("button", { name: "Restart saved environment", exact: true })).toBeDisabled();
  await controls.getByRole("checkbox").check();
  let lost = false;
  const calls: unknown[] = [];
  await page.route("**/api/admin/projects/admin-controls-browser/pause", async route => {
    calls.push(route.request().postDataJSON());
    if (!lost) { lost = true; const response = await route.fetch(); expect(response.status()).toBe(202); await response.dispose(); await route.abort("failed"); return; }
    await route.continue();
  });
  await controls.getByRole("button", { name: "Pause Project", exact: true }).click();
  await controls.getByRole("button", { name: "Check original control request", exact: true }).click();
  await expect(controls.getByText("Operation accepted. Runtime completion is confirmed separately by observation.", { exact: true })).toBeVisible();
  expect(calls).toHaveLength(2); expect(calls[0]).toEqual(calls[1]);
  await controls.getByRole("link", { name: "View runtime control operation", exact: true }).click();
  await expect(page.getByRole("heading", { name: "project.pause", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "admin-controls-browser", exact: true })).toBeVisible();
  await expect(page.getByText("stopping", { exact: true })).toBeVisible();
  const detail = await (await page.request.get("/api/admin/projects/admin-controls-browser")).json();
  expect(detail.binding).toBeNull(); expect(detail.execution_suspended).toBe(true); expect(detail.control_pending).toBe(true);
});
