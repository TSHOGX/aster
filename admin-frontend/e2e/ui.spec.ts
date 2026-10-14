import { chooseValue } from "./picker";
import { expect, test } from "@playwright/test";
import { adminFixture } from "./fixture";
const account = { id: "admin-id", username: "admin", display_name: "Administrator", role: "admin", status: "active", revision: 1, session_revision: 1, must_change_password: false };
test("production admin deep links keep every script under the admin base path", async ({ page }) => {
  await adminFixture(page);
  await page.goto("/admin/users/user-id");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("ordinary");
  const scripts = await page.locator("script[src]").evaluateAll(nodes => nodes.map(node => node.getAttribute("src")!));
  expect(scripts.filter(src => src.includes("/_next/static/")).every(src => src.startsWith("/admin/_next/static/"))).toBe(true);
  await page.reload(); await expect(page.getByRole("heading", { level: 1 })).toHaveText("ordinary");
});
test("non-admin access is denied without a login redirect loop", async ({ page }) => {
  const fixture = await adminFixture(page, "user"); await page.goto("/admin/users");
  await expect(page.getByRole("heading", { name: "Access denied" })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/admin/users");
  expect(fixture.calls.filter(call => call.path.startsWith("/api/admin/"))).toEqual([]);
  await expect(page.getByRole("link", { name: "Return to workbench" })).toHaveAttribute("href", "/");
});
for (const restricted of [false, true]) test(`login and password setup return to the requested admin page (${restricted})`, async ({ page }) => {
  await page.route("**/api/auth/session", route => route.fulfill(restricted ? { json: { ...account, must_change_password: true } } : { status: 401, json: { error: "login required" } }));
  await page.goto("/admin/users/user-id?tab=security");
  await page.waitForURL(url => url.pathname === "/" && url.searchParams.get("return_to") === "/admin/users/user-id?tab=security");
});
for (const width of [390, 1440]) test(`create, edit and one-time credential forms fit ${width}px`, async ({ page }, info) => {
  const fixture = await adminFixture(page); await page.setViewportSize({ width, height: 900 });
  await page.goto("/admin/users/new");
  await page.getByLabel("Username", { exact: true }).fill("u".repeat(64));
  await page.getByLabel("Display name", { exact: true }).fill("N".repeat(128));
  await chooseValue(page.getByRole("combobox", { name: "Membership", exact: true }), "none");
  await page.getByRole("button", { name: "Create account", exact: true }).click();
  const dialog = page.getByRole("dialog"); await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel("Temporary password", { exact: true })).toHaveValue("temporary-password-value");
  expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
  await expect(dialog.getByLabel("Temporary password", { exact: true })).toBeFocused();
  await page.keyboard.press("Shift+Tab"); await expect(dialog.getByRole("button", { name: "Done", exact: true })).toBeFocused();
  await page.keyboard.press("Tab"); await expect(dialog.getByLabel("Temporary password", { exact: true })).toBeFocused();
  expect(fixture.calls.find(call => call.path === "/api/admin/users" && call.method === "POST")!.body!.group_ids).toEqual([]);
  await dialog.getByRole("button", { name: "Done", exact: true }).click();
  await page.waitForURL(url => url.pathname === "/admin/users/created-id");
  await expect(page.getByLabel("Temporary password", { exact: true })).toHaveCount(0);
  await page.getByLabel("Display name", { exact: true }).fill("Updated user");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Updated user");
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1); expect(overflow).toBe(false);
  await page.screenshot({ path: info.outputPath("user-details.png") });
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain("temporary-password-value");
});
test("lost create reply retries the original request without duplicating or replaying a password", async ({ page }) => {
  const fixture = await adminFixture(page); fixture.controls.loseCreateReply = true;
  await page.goto("/admin/users/new"); await page.getByLabel("Username", { exact: true }).fill("lost-user"); await page.getByLabel("Display name", { exact: true }).fill("Lost user");
  await page.getByRole("button", { name: "Create account", exact: true }).click();
  await page.getByRole("button", { name: "Check original request", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("password cannot be shown again");
  const calls = fixture.calls.filter(call => call.method === "POST" && call.path === "/api/admin/users"); expect(calls).toHaveLength(2); expect(calls[0].body).toEqual(calls[1].body);
  expect(fixture.users.filter(user => user.username === "lost-user")).toHaveLength(1); await expect(page.getByRole("dialog")).toHaveCount(0);
});
test("stale edits retain the changed field and preserve an unrelated new role", async ({ page }) => {
  const fixture = await adminFixture(page); fixture.controls.conflictEdit = true;
  await page.goto("/admin/users/user-id"); await page.getByLabel("Display name", { exact: true }).fill("Reapplied name");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page.locator("main").getByRole("alert")).toContainText("changed elsewhere");
  await expect(page.getByRole("combobox", { name: "Management role", exact: true })).toHaveAttribute("data-value", "admin");
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue("Reapplied name");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  const calls = fixture.calls.filter(call => call.method === "PATCH"); expect(calls[1].body).toMatchObject({ expected_revision: 2, display_name: "Reapplied name" }); expect(calls[1].body).not.toHaveProperty("role");
});
test("audit filters and accepted operations report observed state", async ({ page }) => {
  const fixture = await adminFixture(page); await page.goto("/admin/audit");
  await page.getByLabel("Actor ID", { exact: true }).fill("admin-id"); await page.getByRole("button", { name: "Filter", exact: true }).click();
  await expect(page.getByText("account.create", { exact: true })).toBeVisible(); expect(fixture.calls.some(call => call.path === "/api/admin/audit-events")).toBe(true);
  await page.goto("/admin/operations/operation"); await expect(page.getByRole("status")).toHaveText("accepted");
  await expect(page.getByText("The request is recorded. Resource work has not been confirmed complete.")).toBeVisible();
});

for(const width of [390,1440]) test(`group policy bindings, membership and deletion impact fit ${width}px`,async({page},info)=>{
  const fixture=await adminFixture(page);await page.setViewportSize({width,height:900});
  await page.goto("/admin/groups/new");
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Researchers");
  await page.getByRole("textbox",{name:"Description",exact:true}).fill("Research group");
  await page.getByRole("button",{name:"Save group",exact:true}).click();
  await expect(page.getByRole("heading",{level:1})).toHaveText("Researchers");
  await page.getByLabel("Standard operations",{exact:true}).check();
  await page.getByRole("button",{name:"Save policy bindings",exact:true}).click();
  await chooseValue(page.getByRole("combobox",{name:"Account to add",exact:true}), "user-id");
  await page.getByRole("button",{name:"Add member",exact:true}).click();
  await expect(page.getByRole("button",{name:"Remove ordinary",exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Preview deletion",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Grants lost",exact:true})).toBeVisible();
  await expect(page.locator(".impact-card")).toContainText("agents.run");
  await page.getByText("Resource entitlement changes",{exact:true}).click();
  await page.screenshot({path:info.outputPath("group-impact.png"),fullPage:true});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  fixture.controls.stalePreview=true;
  await page.getByRole("checkbox",{name:/I reviewed the impact/}).check();
  await page.getByRole("button",{name:"Delete group",exact:true}).click();
  await expect(page.getByRole("alert").filter({hasText:"Preview deletion again"})).toBeVisible();
  await page.getByRole("button",{name:"Preview deletion",exact:true}).click();
  await page.getByRole("checkbox",{name:/I reviewed the impact/}).check();
  await page.getByRole("button",{name:"Delete group",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Group deleted",exact:true})).toBeVisible();
});

test("policy creation retries original intent and stale edits retain only changed fields",async({page})=>{
  const fixture=await adminFixture(page);fixture.controls.losePolicyReply=true;
  await page.goto("/admin/policies/new");
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Export denial");
  await chooseValue(page.getByRole("combobox",{name:"files.export",exact:true}), "deny");
  await page.getByRole("button",{name:"Save policy",exact:true}).click();
  await page.getByRole("button",{name:"Check original request",exact:true}).click();
  await expect(page.getByRole("status").filter({hasText:"Original change confirmed."})).toHaveText("Original change confirmed.");
  expect(fixture.policies.filter(policy=>policy.name==="Export denial")).toHaveLength(1);
  const creates=fixture.calls.filter(call=>call.path==="/api/admin/policies"&&call.method==="POST");expect(creates).toHaveLength(2);expect(creates[1].body).toEqual(creates[0].body);
  fixture.controls.conflictPolicy=true;
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Edited denial");
  await page.getByRole("button",{name:"Save policy",exact:true}).click();
  await page.getByRole("button",{name:"Reload current data",exact:true}).click();
  await expect(page.getByRole("textbox",{name:"Description",exact:true})).toHaveValue("Concurrent description");
  await expect(page.getByRole("textbox",{name:"Name",exact:true})).toHaveValue("Edited denial");
  await page.getByRole("button",{name:"Save policy",exact:true}).click();
  await expect(page.getByRole("heading",{level:1})).toHaveText("Edited denial");
  await page.getByRole("textbox",{name:"Name",exact:true}).fill("Unsaved");
  page.once("dialog",dialog=>dialog.dismiss());
  await page.getByRole("link",{name:"Groups",exact:true}).click();
  await expect(page.getByRole("textbox",{name:"Name",exact:true})).toHaveValue("Unsaved");
});

test("effective access explains each policy source",async({page})=>{
  await adminFixture(page);await page.goto("/admin/users/user-id");
  const access=page.getByRole("region",{name:"Effective access"});
  await expect(access.getByRole("row").filter({has:page.getByRole("cell",{name:"files.read",exact:true})})).toContainText("Allowed");
  await expect(access.getByRole("row").filter({has:page.getByRole("cell",{name:"admin.content.read",exact:true})})).toContainText("No grant");
  await expect(access.getByRole("link",{name:"Standard operations"}).first()).toHaveAttribute("href","/admin/policies/standard-policy");
});

test("revoked inspection cannot display a late content response",async({page})=>{
  await adminFixture(page);
  await page.route("**/api/me/capabilities",route=>route.fulfill({json:{account_id:"admin-id",account_status:"active",management_role:"admin",revision:1,actions:{"admin.content.read":{allowed:true}}}}));
  let release!:()=>void;const held=new Promise<void>(resolve=>{release=resolve;});let waiting=false;
  await page.route("**/conversations/private-session/inspect",async route=>{waiting=true;await held;await route.fulfill({json:{account_id:"user-id",project_id:"private-project",session_id:"private-session",title:"Secret title",audit_id:1,messages:[{id:"one",role:"user",content:"Late private content",sequence:1,created_at:"2026-10-11",truncated:false}]}});});
  await page.goto("/admin/users/user-id");const panel=page.getByRole("region",{name:"Private conversation inspection"});
  await panel.getByRole("textbox",{name:"Conversation ID",exact:true}).fill("private-session");await panel.getByRole("textbox",{name:"Inspection purpose",exact:true}).fill("Case 99");await panel.getByRole("button",{name:"Inspect conversation",exact:true}).click();
  await expect.poll(()=>waiting).toBe(true);
  await page.evaluate(()=>window.dispatchEvent(new Event("admin-authority-denied")));
  release();
  await expect(panel).toContainText("An explicit admin.content.read grant is required");await expect(panel).not.toContainText("Late private content");
});

test("environment templates use registered inventory and lock required publications", async ({ page }, info) => {
 await adminFixture(page); await page.setViewportSize({width:390,height:900});
 const digest=`sha256:${"a".repeat(64)}`,reference={id:"runtime",version:"v1",digest};
 const required=[{role:"agent_config",destination:"/opt/agent-config",artifact_id:"agent_config"},{role:"shared_assets",destination:"/opt/shared-assets",artifact_id:"shared_assets"}];
 await page.route("**/api/admin/runtime/options?*",route=>route.fulfill({json:{max_mounts:"5",options:new URL(route.request().url()).searchParams.get("kind")==="sandbox"?[{reference,kind:"sandbox",enabled:true,availability:"available",required_publications:required}]:required.map(item=>({reference:{id:item.artifact_id,version:"v1",digest},kind:"volume",roles:[item.role],enabled:true,availability:"available"}))}}));
 await page.goto("/admin/resources/environment");
 await page.getByRole("button",{name:"Create template",exact:true}).click();
 await expect(page.getByLabel("Mount path",{exact:true}).nth(2)).toHaveAttribute("readonly","");
 await expect(page.getByRole("button",{name:"Remove directory",exact:true})).toHaveCount(0);
 await page.getByRole("button",{name:"Add data directory",exact:true}).click();
 await expect(page.getByRole("button",{name:"Add data directory",exact:true})).toBeDisabled();
 await chooseValue(page.getByRole("combobox",{name:"Storage",exact:true}), "owned");
 await page.getByLabel("Capacity (bytes)",{exact:true}).fill("9007199254740993");
 await expect(page.getByLabel("Capacity (bytes)",{exact:true})).toHaveValue("9007199254740993");
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
 await page.screenshot({path:info.outputPath("environment-template-mobile.png"),fullPage:true});
 await page.getByLabel("Name",{exact:true}).fill("Shared configuration");
 await chooseValue(page.getByRole("combobox",{name:"Storage",exact:true}),"shared");
 await page.getByRole("button",{name:"Validate and review",exact:true}).click();
 await expect(page.getByRole("combobox",{name:"Shared volume",exact:true})).toBeFocused();
 await expect(page.getByRole("combobox",{name:"Shared volume",exact:true})).toHaveAttribute("aria-invalid","true");
 const source=page.getByRole("combobox",{name:"Published version",exact:true}).first();
 await source.click();
 await expect(page.getByRole("option",{name:"Select an initialization source",exact:true})).toHaveAttribute("aria-disabled","true");
 await page.keyboard.press("Home");await page.keyboard.press("Enter");
 await expect(source).toHaveAttribute("data-value",JSON.stringify(["agent_config","v1",digest]));
});

test("Project Volume metadata distinguishes allocated initialization from reservation on mobile", async ({ page }, info) => {
  await adminFixture(page); await page.setViewportSize({ width: 390, height: 900 });
  const digest = `sha256:${"a".repeat(64)}`;
  const slot = { key: "workspace", role: "workspace", destination: "/workspaces/project", storage: "owned", access: "read_write", capacity_bytes: "9007199254740993", source_minimum_bytes: "0", clean_runtime: false, volume_id: "recorded-volume", state: "initializing", allocated_bytes: "9007199254740993", reserved_bytes: "0", observation: "unavailable" };
  await page.route("**/api/admin/projects/partial", route => route.fulfill({ json: {
    id: "partial", title: "Partial initialization", owner_id: "user-id", owner_username: "ordinary", owner_status: "disabled", state: "blocked", revision: 2, updated_at: "2026-10-11T00:00:00Z", bound: true, environment_revision: 4, sandbox_release: { id: "runtime", version: "v1", digest }, allocated_bytes: "9007199254740993", reserved_bytes: "1048576", slot_count: "2",
    binding: { resource_revision: 3, store_id: "diagnostic-store", template_id: "diagnostic-template", epoch: "0", occupied: false, cpu_millis: "1000", memory_bytes: "1073741824", overlay_bytes: "268435456", slots: [slot, { ...slot, key: "runtime", role: "runtime", destination: "/state/project", capacity_bytes: "1048576", allocated_bytes: "0", reserved_bytes: "1048576", volume_id: undefined, state: "reserved", observation: "not_materialized" }] },
    desired_network: { revision: digest, direct: [], resolvers: [] }, network_mode: "inherit", saved_network_revision: digest, network_pending: true, observation: { status: "unavailable" }, operation: { id: "pending-operation", kind: "create", phase: "volume_initialization", state: "unknown", error: "Create reply was lost", created_at: "2026-10-11T00:00:00Z", attempted_at: "2026-10-11T00:00:01Z" }, admin_operations: ["suspension-operation"],
  } }));
  await page.goto("/admin/projects/partial");
  await expect(page.getByRole("heading", { name: "Partial initialization", exact: true })).toBeVisible();
  await expect(page.getByText("9,007,199,254,740,993 bytes", { exact: true })).toBeVisible();
  await expect(page.getByText("initializing", { exact: true })).toBeVisible();
  await expect(page.getByText("reserved", { exact: true })).toBeVisible();
  await expect(page.getByText("Not confirmed by this observation", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "suspension-operation", exact: true })).toHaveAttribute("href", "/admin/operations/suspension-operation");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.getByRole("heading", { name: "Volume slots", exact: true }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath("partial-volume-metadata-mobile.png") });
});

for (const width of [390, 1440]) test(`resource templates use shared pickers and preserve reviewed intent at ${width}px`, async ({ page }, info) => {
  await adminFixture(page);
  await page.setViewportSize({ width, height: 900 });
  const writes: Record<string, unknown>[] = [];
  let saved = false, failRefresh = true;
  await page.route("**/api/admin/resource-templates/network", route => {
    if (route.request().method() === "GET") {
      if (saved && failRefresh) { failRefresh = false; return route.fulfill({ status: 503, json: { error: "Refresh unavailable" } }); }
      return route.fulfill({ json: { templates: [] } });
    }
    const body = route.request().postDataJSON(); writes.push(body);
    if (body.preview) return route.fulfill({ json: { expected_dependencies: "reviewed", projects: [], users: [], proposed: {} } });
    saved = true; return route.fulfill({ json: { id: "new-network" } });
  });
  await page.goto("/admin/resources/network");
  await page.getByRole("button", { name: "Create template", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill("Research network");
  await page.getByRole("button", { name: "Add destination", exact: true }).click();
  await page.getByLabel("IPv4 CIDR", { exact: true }).fill("192.0.2.0/24");
  await page.getByLabel("DNS resolvers", { exact: true }).fill(" 1.1.1.1 \n\n");
  expect(await page.getByRole("combobox", { name: "Protocol", exact: true }).evaluate(node => node.tagName)).toBe("BUTTON");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("resource-template.png"), fullPage: true });
  await page.getByRole("button", { name: "Validate and review", exact: true }).click();
  expect(writes[0].network).toMatchObject({ resolvers: ["1.1.1.1"] });
  await page.getByRole("button", { name: "Confirm and save", exact: true }).click();
  await expect(page.getByText("Changes were saved. Reload the current data before editing again.")).toBeVisible();
  await expect(page.getByLabel("Name", { exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Reload saved data", exact: true }).click();
  await expect(page.getByRole("button", { name: "Create template", exact: true })).toBeVisible();
  expect(writes).toHaveLength(2);
  expect(writes[1].request_id).toBe(writes[0].request_id);
  expect(writes[1].expected_dependencies).toBe("reviewed");
});

test("administrator configuration preserves an untouched override and supports clearing it", async ({ page }) => {
  await adminFixture(page);
  const network = { id: "network-one", name: "Network one", enabled: true, description: "", sources: ["group"] };
  const environment = { ...network, id: "environment-one", name: "Environment one" };
  const value = { default_revision: 3, administrator_defaults: { network_template_id: network.id, environment_template_id: environment.id }, default_network: network, default_environment: environment, networks: [network], environments: [environment], shared_volumes: [], needs_admin_resolution: false };
  await page.route("**/api/admin/users/user-id/configuration", route => route.fulfill({ json: value }));
  let body: Record<string, unknown> = {};
  await page.route("**/api/admin/users/user-id/configuration-defaults", route => { body = route.request().postDataJSON(); return route.fulfill({ json: { expected_dependencies: "review", projects: [], users: [] } }); });
  await page.goto("/admin/users/user-id/configuration");
  await expect(page.getByRole("combobox", { name: "Network default", exact: true })).toHaveAttribute("data-value", network.id);
  await expect(page.getByRole("button", { name: "Review administrator defaults", exact: true })).toBeDisabled();
  await chooseValue(page.getByRole("combobox", { name: "Network default", exact: true }), "");
  await page.getByRole("button", { name: "Review administrator defaults", exact: true }).click();
  expect(body).toMatchObject({ network_template_id: "", environment_template_id: environment.id, expected_revision: 3, preview: true });
});

test("settings navigation keeps the admin shell and theme mounted without reauthentication", async ({page}) => {
  const fixture = await adminFixture(page);
  let documents = 0;
  page.on("request", request => { if (request.isNavigationRequest() && request.frame() === page.mainFrame()) documents++; });
  await page.goto("/admin/resources/network");
  await expect(page.getByRole("heading", {name:"Resource Center",exact:true})).toBeVisible();
  const header = await page.locator(".admin-header").elementHandle();
  const tabs = await page.locator(".catalog-tabs").elementHandle();
  const reads = fixture.calls.filter(call => call.path === "/api/auth/session").length;
  await page.route("**/api/admin/resource-templates/environment", async route => {
    await new Promise(resolve => setTimeout(resolve, 300));
    await route.fulfill({json:{templates:[]}});
  });
  for (const name of ["Environment templates", "Network templates"]) {
    await page.getByRole("navigation",{name:"Resource categories"}).getByRole("link",{name,exact:true}).click();
    await expect(page.getByRole("navigation",{name:"Resource categories"}).getByRole("link",{name,exact:true})).toHaveAttribute("aria-current","page");
    expect(await header!.evaluate(node => node.isConnected)).toBe(true);
    expect(await tabs!.evaluate(node => node.isConnected)).toBe(true);
    await expect(page.getByText("Opening administration…",{exact:true})).toHaveCount(0);
  }
  for(const name of ["Users","Groups","Policies"]){
    await page.getByRole("navigation",{name:"Administration",exact:true}).getByRole("link",{name,exact:true}).click();
    await expect(page.getByRole("heading",{level:1})).toHaveText(name);
    expect(await header!.evaluate(node => node.isConnected)).toBe(true);
  }
  expect(fixture.calls.filter(call => call.path === "/api/auth/session")).toHaveLength(reads);
  expect(documents).toBe(1);
});

for(const width of [390,1440]) test(`shared dropdown keyboard and minimal selected state at ${width}px`,async({page},info)=>{
 await adminFixture(page);await page.setViewportSize({width,height:900});await page.goto("/admin/users/new");
 const role=page.getByRole("combobox",{name:"Management role",exact:true});
 await role.focus();await page.keyboard.press("ArrowDown");
 const list=page.getByRole("listbox",{name:"Management role",exact:true});await expect(list).toBeVisible();
 await page.keyboard.press("End");await page.keyboard.press("Enter");
 await expect(role).toHaveAttribute("data-value","admin");await expect(role).toBeFocused();
 await role.click();await page.keyboard.press("u");await page.keyboard.press("Enter");await expect(role).toHaveAttribute("data-value","user");
 await role.click();await page.keyboard.press("Escape");await expect(list).toHaveCount(0);await expect(role).toBeFocused();
 await role.click();await page.keyboard.press("Tab");await expect(page.getByRole("combobox",{name:"Membership",exact:true})).toBeFocused();
 await role.click();expect(await list.evaluate(node=>{const r=node.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.bottom<=innerHeight})).toBe(true);
 await page.screenshot({path:info.outputPath("shared-dropdown.png")});
 await page.keyboard.press("Escape");
 expect(await role.evaluate(node=>getComputedStyle(node).textDecorationLine)).toBe("none");
 await page.getByRole("navigation",{name:"Administration",exact:true}).getByRole("link",{name:"Resource Center",exact:true}).click();
 const active=page.getByRole("navigation",{name:"Resource categories"}).getByRole("link",{name:"Network templates",exact:true});
 expect(await active.evaluate(node=>getComputedStyle(node).boxShadow)).toBe("none");
 expect(await active.evaluate(node=>getComputedStyle(node).textDecorationLine)).toBe("none");
});

test("environment updates separate save from release and preview project differences", async ({ page }, info) => {
  await adminFixture(page); await page.setViewportSize({width:390,height:900});
  const digest=`sha256:${"a".repeat(64)}`,reference={id:"runtime",version:"v1",digest};
  const mount=(version:string)=>({key:"knowledge",role:"data",destination:"/knowledge",storage:"shared_readonly",access:"read_only",source:{kind:"builtin",artifact_id:"knowledge",version,digest},capacity:{}});
  const template={id:"research",name:"Research",description:"",kind:"environment",revision:2,enabled:true,builtin:false,environment:{revision:2,sandbox_release:reference,mounts:[{key:"workspace",role:"workspace",destination:"/workspace",storage:"owned",access:"read_write",source:{kind:"empty"},capacity:{setting:"workspace_bytes"}},{key:"runtime",role:"runtime",destination:"/runtime",storage:"owned",access:"read_write",source:{kind:"empty"},capacity:{setting:"runtime_bytes"}},mount("v2")]}};
  const project={project_id:"project-a",title:"Research notes",environment_id:"research",policy:"follow",revision:1,target_id:"",applied_id:"",state:"pending",reason:"",attempts:0,updated_at:"",current:[mount("v1")],target:[mount("v2")]};
  let released=false;const writes:Record<string,unknown>[]=[];
  const release={id:"release-1",sequence:1,revision:1,configuration_revision:2,state:"active",note:"Updated knowledge",created_at:"2026-10-13T00:00:00Z",mounts:[mount("v2")]};
  await page.route("**/api/admin/resource-templates/environment",route=>route.fulfill({json:{templates:[template]}}));
  await page.route("**/api/admin/runtime/options?*",route=>route.fulfill({json:{max_mounts:"8",options:new URL(route.request().url()).searchParams.get("kind")==="sandbox"?[{kind:"sandbox",reference,enabled:true,availability:"available"}]:[{kind:"volume",reference:{id:"knowledge",version:"v2",digest},roles:["data"],enabled:true,availability:"available"}]}}));
  await page.route("**/api/admin/environments/research/updates**",async route=>{
    if(route.request().method()==="POST"){
      const body=route.request().postDataJSON();writes.push(body);
      if(body.preview)return route.fulfill({json:{expected_dependencies:"reviewed",proposed:{release,counts:{pending:1}},users:[],projects:[]}});
      expect(body.expected_dependencies).toBe("reviewed");released=true;return route.fulfill({json:{release,counts:{pending:1}}});
    }
    return route.fulfill({json:{releases:released?[release]:[],projects:[project],counts:{pending:1}}});
  });
  await page.goto("/admin/resources/environment");
  await page.getByRole("button",{name:"Edit",exact:true}).click();
  await page.getByRole("button",{name:"Updates",exact:true}).click();
  await expect(page.getByText("No updates published.",{exact:false})).toBeVisible();
  await page.getByLabel("Release note").fill("Updated knowledge");
  await page.getByRole("button",{name:"Preview update",exact:true}).click();
  await expect(page.getByRole("region",{name:"Review environment update"})).toBeVisible();
  expect(released).toBe(false);
  await page.getByText("Review affected projects and version differences",{exact:true}).click();
  await expect(page.getByText("Research notes · Waiting for next start or resume",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Confirm",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Update 1",exact:true})).toBeVisible();
  expect(writes).toHaveLength(2);
  await page.getByRole("button",{name:"Projects",exact:true}).click();
  await expect(page.getByRole("link",{name:"Research notes",exact:true})).toBeVisible();
  await expect(page.getByRole("button",{name:"Keep current versions",exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
  await page.screenshot({path:info.outputPath("environment-update-projects-mobile.png"),fullPage:true});
  const status = page.getByRole("combobox", { name: "Status", exact: true });
  let fail = true;
  await page.route("**/api/admin/environments/research/updates?*", route => {
    if (fail) return route.fulfill({ status: 503, json: { error: "Temporary update outage" } });
    return route.fulfill({ json: { releases: [release], projects: [{ ...project, state: "failed" }], counts: { failed: 1 } } });
  });
  await chooseValue(status, "failed");
  await expect(page.getByRole("alert").filter({ hasText: "Temporary update outage" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Research notes", exact: true })).toHaveCount(0);
  fail = false;
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("link", { name: "Research notes", exact: true })).toBeVisible();
  await expect(page.getByRole("alert").filter({ hasText: "Temporary update outage" })).toHaveCount(0);
});

test("primary links and buttons retain readable hover contrast across themes and accents", async ({ page }) => {
  await adminFixture(page);
  await page.goto("/admin/users");
  const create = page.getByRole("link", { name: "Create user", exact: true });
  await expect(create).toBeVisible();
  await expect(page.getByRole("link", { name: "ordinary", exact: true })).toHaveCSS("text-decoration-line", "underline");
  const contrast = (control: import("@playwright/test").Locator) => control.evaluate(node => {
    const style = getComputedStyle(node);
    const luminance = (color: string) => {
      const rgb = color.match(/[\d.]+/g)!.slice(0, 3).map(Number).map(v => {
        const n = v / 255; return n <= .04045 ? n / 12.92 : ((n + .055) / 1.055) ** 2.4;
      });
      return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722;
    };
    const a = luminance(style.color), b = luminance(style.backgroundColor);
    return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
  });
  for (const theme of ["light", "dark"]) for (const accent of ["neutral", "blue", "green", "violet"]) {
    await page.evaluate(({ theme, accent }) => { document.documentElement.dataset.theme = theme; document.documentElement.dataset.accent = accent; }, { theme, accent });
    await create.hover();
    expect(await contrast(create), `${theme}/${accent} primary link hover`).toBeGreaterThanOrEqual(4.5);
    await create.focus();
    expect(await contrast(create), `${theme}/${accent} primary link focus`).toBeGreaterThanOrEqual(4.5);
  }
  await create.click();
  await page.getByLabel("Username", { exact: true }).fill("contrast-user");
  const button = page.getByRole("button", { name: "Create account", exact: true });
  for (const theme of ["light", "dark"]) for (const accent of ["neutral", "blue", "green", "violet"]) {
    await page.evaluate(({ theme, accent }) => { document.documentElement.dataset.theme = theme; document.documentElement.dataset.accent = accent; }, { theme, accent });
    await button.hover();
    expect(await contrast(button), `${theme}/${accent} primary button hover`).toBeGreaterThanOrEqual(4.5);
  }
});

test("expanding resource constraints keeps page and table columns stable", async ({ page }) => {
  await adminFixture(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/admin/runtime/options?*", route => route.fulfill({ json: {
    max_mounts: "8", options: [{ kind: "sandbox", reference: { id: "stable-runtime", version: "v1", digest: `sha256:${"a".repeat(64)}` }, enabled: true, availability: "available", required_publications: Array.from({ length: 20 }, (_, i) => ({ role: "data", destination: `/reference/${i}/${"long-path-".repeat(15)}` })) }],
  } }));
  await page.goto("/admin/resources/sandboxes");
  const toggle = page.getByText("View constraints", { exact: true });
  await expect(toggle).toBeVisible();
  const geometry = () => page.locator("th, .page-heading").evaluateAll(nodes => nodes.map(node => {
    const rect = node.getBoundingClientRect(); return { left: rect.left, width: rect.width };
  }));
  const before = await geometry();
  await toggle.click();
  expect(await geometry()).toEqual(before);
  await toggle.click();
  expect(await geometry()).toEqual(before);
});

for (const width of [390, 1440]) test(`Admin follows account language and preserves edits at ${width}px`, async ({ page }, info) => {
  await adminFixture(page);
  await page.setViewportSize({ width, height: 900 });
  let preferences = { revision: 1, theme_mode: "system", accent: "neutral", language: "en" };
  await page.route("**/api/me/preferences", route => route.fulfill({ json: preferences }));
  await page.goto("/admin/users/user-id");
  await page.getByLabel("Display name", { exact: true }).fill("Unsaved English Name");
  preferences = { ...preferences, revision: 2, language: "zh" };
  await page.evaluate(() => window.dispatchEvent(new StorageEvent("storage", { key: "account-appearance:admin-id" })));
  await expect(page.getByRole("link", { name: "资源中心", exact: true })).toBeVisible();
  await expect(page.getByLabel("显示名称", { exact: true })).toHaveValue("Unsaved English Name");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
  await expect(page.getByRole("combobox", { name: "语言", exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  await page.screenshot({ path: info.outputPath(`admin-zh-${width}.png`) });
  await page.getByRole("button", { name: "保存更改", exact: true }).click();
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Unsaved English Name");
});

test("Chinese browser preferences apply throughout Admin navigation and policy controls", async ({ browser }) => {
  const context = await browser.newContext({ locale: "zh-CN", viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  await adminFixture(page);
  await page.goto("/admin/policies/standard-policy");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByRole("combobox", { name: "运行智能体", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "审计", exact: true }).click();
  await expect(page.getByRole("heading", { name: "审计", exact: true })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
  await context.close();
});

for (const width of [390, 1440]) test(`bilingual shared-assets rollout retains a reviewed cohort after a lost response at ${width}px`,async({page},info)=>{
 await adminFixture(page);
 await page.setViewportSize({ width, height: 900 });
 let preferences = { revision: 1, theme_mode: "system", accent: "neutral", language: "en" };
 await page.route("**/api/me/preferences", route => route.fulfill({ json: preferences }));
 const digest=`sha256:${"b".repeat(64)}`,reference={id:"runtime",version:"v1",digest};
 const mount=(version:string)=>({key:"context",role:"shared_assets",destination:"/opt/shared-assets",storage:"shared_readonly",access:"read_only",source:{kind:"builtin",artifact_id:"context",version,digest},capacity:{}});
 const template={id:"team",name:"Team environment",description:"",kind:"environment",revision:2,enabled:true,builtin:false,environment:{revision:2,sandbox_release:reference,mounts:[mount("v2")]}};
 const project=(id:string,state="pending")=>({project_id:id,title:id,environment_id:"team",policy:"follow",revision:1,target_id:"",applied_id:"",state,reason:state==="pending"?"":"Target unavailable",attempts:0,updated_at:"",current:[mount("v1")],target:[mount("v2")]});
 const release={id:"rollout",kind:"system_content_replace",sequence:1,revision:1,configuration_revision:2,state:"active",note:"",created_at:"2026-10-14T00:00:00Z",mounts:[mount("v2")]};
 let lost=false,saved=false;const commits:Record<string,unknown>[]=[];
 await page.route("**/api/admin/resource-templates/environment",route=>route.fulfill({json:{templates:[template]}}));
 await page.route("**/api/admin/runtime/options?*",route=>route.fulfill({json:{max_mounts:"8",options:new URL(route.request().url()).searchParams.get("kind")==="sandbox"?[{kind:"sandbox",reference,enabled:true,availability:"available"}]:[]}}));
 await page.route("**/api/admin/environments/team/updates**",async route=>{
  if(route.request().method()==="POST"){
   const body=route.request().postDataJSON();expect(body.kind).toBe("system_content_replace");expect(body.project_ids).toEqual(["Selected Project"]);
   const result={can_publish:true,release,projects:[project("Selected Project")],counts:{pending:1}};
   if(body.preview)return route.fulfill({json:{expected_dependencies:"reviewed",proposed:result,users:[],projects:[]}});
   commits.push(body);saved=true;if(!lost){lost=true;return route.abort("failed")};return route.fulfill({json:result});
  }
  return route.fulfill({json:{releases:saved?[release]:[],projects:[project("Selected Project"),project("Unselected Project"),project("Blocked Project","incompatible")],counts:{pending:2,incompatible:1}}});
 });
 await page.goto("/admin/resources/environment");await page.getByRole("button",{name:"Edit",exact:true}).click();await page.getByRole("button",{name:"Updates",exact:true}).click();
 await chooseValue(page.getByRole("combobox",{name:"Update content",exact:true}),"system_content_replace");
 await expect(page.getByRole("button",{name:"Preview update",exact:true})).toBeDisabled();
 await expect(page.getByRole("checkbox",{name:/Blocked Project/})).toBeDisabled();
 await page.getByRole("checkbox",{name:/^Selected Project/}).check();
 await page.getByRole("button",{name:"Preview update",exact:true}).click();
 const review=page.getByRole("region",{name:"Review environment update",exact:true});
 await expect(review).toContainText("Selected Project");await expect(review).not.toContainText("Unselected Project");
 preferences = { ...preferences, revision: 2, language: "zh" };
 await page.evaluate(() => window.dispatchEvent(new StorageEvent("storage", { key: "account-appearance:admin-id" })));
 const chineseReview=page.getByRole("region",{name:"查看环境更新",exact:true});
 await expect(chineseReview).toContainText("Selected Project");
 await expect(chineseReview).not.toContainText("Unselected Project");
 await expect(page.getByRole("checkbox",{name:/^Selected Project/})).toBeChecked();
 await expect(page.getByRole("checkbox",{name:/Blocked Project/})).toBeDisabled();
 await expect(page.getByRole("region",{name:"共享资源更新目标",exact:true})).toContainText("已选择 1 个项目");
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
 await page.screenshot({path:info.outputPath(`shared-assets-review-zh-${width}.png`),fullPage:true});
 await chineseReview.getByRole("button",{name:"确认",exact:true}).click();
 await page.getByRole("button",{name:"检查原请求结果",exact:true}).click();
 await expect(page.getByRole("heading",{name:"更新 1",exact:true})).toBeVisible();
 expect(commits).toHaveLength(2);expect(commits[0]).toEqual(commits[1]);
});
