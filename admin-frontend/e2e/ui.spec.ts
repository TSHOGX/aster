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
  await page.getByRole("combobox", { name: "Membership", exact: true }).selectOption("none");
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
  await expect(page.getByRole("combobox", { name: "Management role", exact: true })).toHaveValue("admin");
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
  await page.getByRole("combobox",{name:"Account to add",exact:true}).selectOption("user-id");
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
  await page.getByRole("combobox",{name:"files.export",exact:true}).selectOption("deny");
  await page.getByRole("button",{name:"Save policy",exact:true}).click();
  await page.getByRole("button",{name:"Check original request",exact:true}).click();
  await expect(page.getByRole("status")).toHaveText("Original change confirmed.");
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

test("runtime editor locks required publications and keeps optional mounts within the bound", async ({ page }, info) => {
  await adminFixture(page); await page.setViewportSize({ width: 390, height: 900 });
  const digest = `sha256:${"a".repeat(64)}`;
  const reference = { id: "runtime", version: "v1", digest };
  const required = [{ role: "agent_config", destination: "/opt/agent-config", artifact_id: "agent_config" }, { role: "shared_assets", destination: "/opt/shared-assets", artifact_id: "shared_assets" }];
  const mounts = [
    { key: "workspace", role: "workspace", destination: "/workspace", storage: "owned", access: "read_write", source: { kind: "empty" }, capacity: { setting: "workspace_bytes" } },
    { key: "runtime", role: "runtime", destination: "/runtime", storage: "owned", access: "read_write", source: { kind: "empty" }, capacity: { setting: "runtime_bytes" } },
    ...required.map(item => ({ key: item.role, role: item.role, destination: item.destination, storage: "shared_readonly", access: "read_only", source: { kind: "builtin", artifact_id: item.artifact_id, version: "v1", digest }, capacity: {} })),
  ];
  await page.route("**/api/admin/runtime/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/default") && route.request().method() === "PATCH") return route.fulfill({ status: 503, json: { code: "artifact_unavailable", error: "registered runtime artifact unavailable" } });
    if (url.pathname.endsWith("/preview")) return route.fulfill({ json: { affected_accounts: "1", accounts_with_plan_conflicts: "1", expected_dependencies: "fingerprint", users: [] } });
    if (url.pathname.endsWith("/default")) return route.fulfill({ json: { revision: 1, sandbox_release: reference, mounts, required_publications: required, max_mounts: "5" } });
    return route.fulfill({ json: { max_mounts: "5", options: url.searchParams.get("kind") === "sandbox" ? [{ reference, kind: "sandbox", enabled: true, availability: "available", required_publications: required, protected_roots: ["/etc", "/proc"], blocked_path_count: 2 }] : required.map(item => ({ reference: { id: item.artifact_id, version: "v1", digest }, kind: "volume", roles: [item.role], enabled: true, availability: "available", minimum_bytes: "1048576", source_capacity_bytes: "8388608" })) } });
  });
  await page.goto("/admin/runtime");
  await expect(page.getByLabel("Mount 3 destination", { exact: true })).toHaveAttribute("readonly", "");
  await expect(page.getByLabel("Mount 3 storage", { exact: true })).toBeDisabled();
  await expect(page.getByLabel("Mount 4 access", { exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Remove agent_config", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Add data mount", exact: true }).click();
  await expect(page.getByRole("button", { name: "Add data mount", exact: true })).toBeDisabled();
  await page.getByLabel("Mount 5 capacity in bytes", { exact: true }).fill("9007199254740993");
  await expect(page.getByLabel("Mount 5 capacity in bytes", { exact: true })).toHaveValue("9007199254740993");
  await page.getByRole("button", { name: "Preview environment changes", exact: true }).click();
  await page.getByRole("checkbox").check();
  await page.getByRole("button", { name: "Save default environment", exact: true }).click();
  await expect(page.locator("main").getByRole("alert")).toContainText("registered runtime artifact unavailable");
  await expect(page.getByLabel("Mount 5 capacity in bytes", { exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Check original request", exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.getByLabel("Mount 3 destination", { exact: true }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: info.outputPath("required-runtime-mounts-mobile.png") });
});
