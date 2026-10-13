import { chooseValue } from "../picker";
import { expect, test, type Page } from "@playwright/test";
import { workspaceFixture } from "./fixture";
import type { Preferences } from "../../../web/appearance";

async function openSettings(page: Page) {
  if ((page.viewportSize()?.width || 1600) < 768)
    await page.getByRole("button", { name: "Open navigation", exact: true }).click();
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("menuitem", { name: "Settings", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Settings", exact: true });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("combobox", { name: "Theme mode", exact: true })).toBeEnabled();
  return dialog;
}

for (const width of [390, 1600]) {
  test(`settings preserve selection and unsaved files and restore focus at ${width}px`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 });
    const fixture = await workspaceFixture(page);
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", { name: "the research report" }).click();
    await page.getByLabel("File actions", { exact: true }).click();
    await page.getByRole("menuitem", { name: "Edit text", exact: true }).click();
    await page.getByLabel("Edit reports/summary.md", { exact: true }).fill("# Retain this unsaved file");
    const location = page.url();
    const dialog = await openSettings(page);
    await page.screenshot({ path: info.outputPath("settings.png") });
    for (let i = 0; i < 16; i++) {
      await page.keyboard.press("Tab");
      expect(await dialog.evaluate(node => node.contains(document.activeElement))).toBe(true);
    }
    await dialog.getByRole("button", { name: "Sign out all sessions", exact: true }).click();
    const guard = page.getByRole("dialog", { name: "Unsaved changes", exact: true });
    await expect(guard).toBeVisible();
    await guard.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(fixture.calls.some(call => call.path === "/api/me/sessions")).toBe(false);
    await dialog.getByRole("button", { name: "Close settings", exact: true }).click();
    await expect(page.getByRole("button", { name: "Account", exact: true })).toBeFocused();
    expect(page.url()).toBe(location);
    if (width < 768) await page.keyboard.press("Escape");
    await expect(page.getByLabel("Edit reports/summary.md", { exact: true })).toContainText("Retain this unsaved file");
  });
}

test("appearance modes and accents persist with readable controls and follow system changes", async ({ page }) => {
  await workspaceFixture(page);
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/?project=battery&session=analysis");
  const dialog = await openSettings(page);
  for (const mode of ["light", "dark", "system"]) {
    await chooseValue(dialog.getByRole("combobox", { name: "Theme mode", exact: true }), mode);
    await expect(dialog.getByRole("combobox", { name: "Theme mode", exact: true })).toBeEnabled();
    for (const accent of ["neutral", "blue", "green", "violet"]) {
      await chooseValue(dialog.getByRole("combobox", { name: "Accent color", exact: true }), accent);
      await expect(dialog.getByRole("combobox", { name: "Accent color", exact: true })).toBeEnabled();
      await expect(page.locator("html")).toHaveAttribute("data-accent", accent);
      await expect(page.locator("html")).toHaveAttribute("data-theme", mode === "system" ? "light" : mode);
      await dialog.getByRole("button", { name: "Change password", exact: true }).hover();
      const contrast = await dialog.getByRole("button", { name: "Change password", exact: true }).evaluate(node => {
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
      expect(contrast).toBeGreaterThanOrEqual(4.5);
    }
  }
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await chooseValue(dialog.getByRole("combobox", { name: "Theme mode", exact: true }), "dark");
  await expect(dialog.getByRole("combobox", { name: "Theme mode", exact: true })).toBeEnabled();
  await page.route("**/api/auth/session", async route => {
    await new Promise(resolve => setTimeout(resolve, 500));
    await route.fallback();
  });
  await page.emulateMedia({ colorScheme: "light" });
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("html")).toHaveAttribute("data-accent", "violet");
  await expect(page.locator(".project-shell")).toBeVisible();
});

test("stale and failed appearance saves keep an explicit preview without erasing unrelated changes", async ({ page }) => {
  await workspaceFixture(page);
  let saved: Preferences = { language: "system", revision: 1, theme_mode: "light", accent: "neutral" };
  let failure: "conflict" | "error" | null = "conflict";
  const mutations: Record<string, unknown>[] = [];
  await page.route("**/api/me/preferences", route => {
    if (route.request().method() === "PATCH") {
      const body = route.request().postDataJSON(); mutations.push(body);
      if (failure === "conflict") {
        saved = { ...saved, revision: 2, theme_mode: "dark" }; failure = null;
        return route.fulfill({ status: 409, json: { error: "changed elsewhere" } });
      }
      if (failure === "error") return route.fulfill({ status: 503, json: { error: "save unavailable" } });
      saved = { language: "system", revision: saved.revision + 1, theme_mode: body.theme_mode ?? saved.theme_mode, accent: body.accent ?? saved.accent };
    }
    return route.fulfill({ json: saved });
  });
  await page.goto("/");
  const dialog = await openSettings(page);
  await chooseValue(dialog.getByRole("combobox", { name: "Accent color", exact: true }), "blue");
  await expect(dialog.getByRole("status")).toHaveText("Unsaved appearance");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator("html")).toHaveAttribute("data-accent", "blue");
  await dialog.getByRole("button", { name: "Reapply changes", exact: true }).click();
  await expect(dialog.getByRole("status")).toHaveText("Saved");
  expect(mutations[1]).toEqual({ expected_revision: 2, accent: "blue" });
  failure = "error";
  await chooseValue(dialog.getByRole("combobox", { name: "Accent color", exact: true }), "green");
  await expect(dialog.getByRole("status")).toHaveText("Unsaved appearance");
  await expect(page.locator("html")).toHaveAttribute("data-accent", "green");
  await dialog.getByRole("button", { name: "Use saved settings", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "blue");
  expect(saved).toEqual({ language: "system", revision: 3, theme_mode: "dark", accent: "blue" });
});

for (const cacheOwner of ["previous-account", "researcher"]) test(`initial preferences use only a cache bound to the same account (${cacheOwner})`, async ({ page }) => {
  await workspaceFixture(page);
  await page.addInitScript(cacheOwner => {
    localStorage.setItem("account-appearance-active", cacheOwner);
    localStorage.setItem(`account-appearance:${cacheOwner}`, JSON.stringify({ account_id: cacheOwner, revision: 4, theme_mode: "dark", accent: "violet" }));
  }, cacheOwner);
  const expected = cacheOwner === "researcher" ? { theme_mode: "dark", accent: "violet" } : { theme_mode: "system", accent: "neutral" };
  let preferences: Preferences = { language: "system", revision: 0, theme_mode: "system", accent: "neutral" };
  await page.route("**/api/me/preferences", route => {
    if (route.request().method() === "PATCH") {
      const body = route.request().postDataJSON();
      expect(body).toEqual({ expected_revision: 0, language: "system", ...expected });
      preferences = { language: "system", revision: 1, theme_mode: body.theme_mode, accent: body.accent };
    }
    return route.fulfill({ json: preferences });
  });
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  await expect(page.locator(".project-shell")).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("data-theme", expected.theme_mode === "system" ? "light" : "dark");
  await expect(page.locator("html")).toHaveAttribute("data-accent", expected.accent);
  await expect.poll(() => preferences.revision).toBe(1);
});

test("same-account tabs converge and logout clears the identity binding", async ({ context, page }) => {
  const other = await context.newPage();
  let signedIn = true;
  let saved: Preferences = { language: "system", revision: 1, theme_mode: "light", accent: "neutral" };
  for (const target of [page, other]) {
    await workspaceFixture(target);
    await target.route("**/api/auth/session", route => route.fulfill(signedIn ? {json:{ id: "researcher", username: "researcher", display_name: "researcher", status: "active", role: "user", revision: 1, session_revision: 1, must_change_password: false }} : {status:401,json:{error:"login required"}}));
    await target.route("**/api/me/sessions", route => { signedIn = false; return route.fulfill({status:204}); });
    await target.route("**/api/me/preferences", route => {
      if (route.request().method() === "PATCH") {
        const body = route.request().postDataJSON();
        saved = { language: "system", revision: saved.revision + 1, theme_mode: body.theme_mode ?? saved.theme_mode, accent: body.accent ?? saved.accent };
      }
      return route.fulfill({ json: saved });
    });
    await target.goto("/");
    await expect(target.locator(".project-shell")).toBeVisible();
  }
  const dialog = await openSettings(page);
  await chooseValue(dialog.getByRole("combobox", { name: "Accent color", exact: true }), "blue");
  await expect(other.locator("html")).toHaveAttribute("data-accent", "blue");
  await dialog.getByRole("button", { name: "Sign out all sessions", exact: true }).click();
  await expect(page.locator(".project-login")).toBeVisible();
  await expect(other.locator(".project-login")).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("account-appearance-active"))).toBeNull();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "neutral");
});

for (const width of [390, 1600]) test(`profile and password forms retain errors, clear secrets and confirm password discard (${width}px)`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 });
  const fixture = await workspaceFixture(page);
  await page.goto("/?project=battery&session=analysis");
  const dialog = await openSettings(page);
  await dialog.getByLabel("Display name", { exact: true }).fill("Researcher Example");
  await dialog.getByRole("button", { name: "Save display name", exact: true }).click();
  await expect(page.getByRole("button", { name: "Account", exact: true })).toContainText("Researcher Example");
  await dialog.getByLabel("Current password", { exact: true }).fill("wrong-password");
  await dialog.getByLabel("New password", { exact: true }).fill("replacement-password");
  await dialog.getByRole("button", { name: "Change password", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("current password is incorrect");
  await dialog.getByLabel("Current password", { exact: true }).fill("current-password");
  await dialog.getByRole("button", { name: "Change password", exact: true }).click();
  await expect(dialog.getByText("Password changed. Other sessions have been signed out.", { exact: true })).toBeVisible();
  await expect(dialog.getByLabel("Current password", { exact: true })).toHaveValue("");
  await expect(dialog.getByLabel("New password", { exact: true })).toHaveValue("");
  await dialog.getByLabel("New password", { exact: true }).fill("discarded-password");
  await page.keyboard.press("Escape");
  const confirm = page.getByRole("dialog", { name: "Discard unsaved account changes?", exact: true });
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(dialog.getByLabel("New password", { exact: true })).toHaveValue("discarded-password");
  await page.keyboard.press("Escape");
  await confirm.getByRole("button", { name: "Discard changes", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Account", exact: true })).toBeFocused();
  expect(fixture.calls.some(call => /\/(stop|pause|restart)$/.test(call.path))).toBe(false);
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }));
  expect(stored).not.toContain("replacement-password");
  expect(stored).not.toContain("discarded-password");
});

for (const documentAccount of ["", "next-account"]) test(`first paint ignores a previous account before session resolution (${documentAccount || "anonymous"})`, async ({ page }) => {
  await workspaceFixture(page);
  await page.setExtraHTTPHeaders(documentAccount ? { "X-Project-Account-ID": documentAccount } : {});
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() => {
    localStorage.setItem("account-appearance-active", "previous-account");
    localStorage.setItem("account-appearance:previous-account", JSON.stringify({ account_id: "previous-account", revision: 3, theme_mode: "dark", accent: "violet" }));
  });
  let releaseSession!: () => void;
  const held = new Promise<void>(resolve => { releaseSession = resolve; });
  await page.route("**/api/auth/session", async route => {
    await held;
    await route.fulfill(documentAccount ? { json: { id: documentAccount, username: "next", display_name: "Next account", status: "active", role: "user", revision: 1, session_revision: 1, must_change_password: false } } : { status: 401, json: { error: "login required" } });
  });
  try {
    await page.goto("/", { waitUntil: "domcontentloaded" });
    expect(await page.locator("html").getAttribute("data-theme")).toBe("light");
    expect(await page.locator("html").getAttribute("data-accent")).toBe("neutral");
    expect(await page.locator("html").getAttribute("data-account-id")).toBe(documentAccount || null);
  } finally { releaseSession(); }
  await expect(page.locator(documentAccount ? ".project-shell" : ".project-login")).toBeVisible();
});

for (const width of [390, 1600]) test(`advanced configuration repairs revoked networks and protects shared edits at ${width}px`, async ({ page }, info) => {
  await page.setViewportSize({ width, height: 1000 });
  await workspaceFixture(page);
  await page.emulateMedia({ colorScheme: width === 390 ? "dark" : "light" });
  const network = { id: "replacement", name: "Research network", enabled: true, description: "", sources: ["researchers"] };
  const environment = { ...network, id: "environment", name: "Research environment" };
  const preferences = { language: "system", revision: 1, network_template_id: "", environment_template_id: "", show_shared_volumes: true };
  await page.route("**/api/me/configuration", route => route.fulfill({ json: { networks: [network], environments: [environment], shared_volumes: [], preferences, default_network: network, default_environment: environment, selected_network: network, selected_environment: environment, network_default_source: [], environment_sources: [], needs_admin_resolution: false } }));
  const project = { project_id: "battery", title: "Battery research", state: "paused", suspended: true, revision: 1, network_template_id: "revoked", network_name: "Old network", environment_name: environment.name };
  await page.route("**/api/me/project-configurations", route => route.fulfill({ json: { projects: [project] } }));
  let networkBody: Record<string, unknown> = {};
  await page.route("**/api/me/project-configurations/battery", route => {
    networkBody = route.request().postDataJSON();
    return route.fulfill({ json: { ...project, revision: 2, network_template_id: network.id, network_name: network.name } });
  });
  await page.route("**/api/projects/battery/mounts", route => route.fulfill({ json: { mounts: [{ key: "shared", name: "Team documents", access: "read_write" }] } }));
  await page.route("**/api/projects/battery/files?*", route => {
    if (new URL(route.request().url()).searchParams.get("mount") !== "shared") return route.fallback();
    return route.fulfill({ json: { entries: [{ name: "notes.txt", kind: "file", size: 5 }] } });
  });
  await page.route("**/api/projects/battery/files/content?*", route => {
    if (new URL(route.request().url()).searchParams.get("mount") !== "shared") return route.fallback();
    return route.fulfill({ body: "original", contentType: "text/plain" });
  });
  await page.goto("/?project=battery&session=analysis");
  const dialog = await openSettings(page);
  await dialog.getByText("Advanced / Developer", { exact: true }).click();
  await expect(dialog.getByRole("combobox", { name: "Network for new projects", exact: true })).toBeVisible();
  await dialog.getByText("Existing projects", { exact: true }).click();
  await chooseValue(dialog.getByRole("combobox", { name: "Project", exact: true }), "battery");
  const select = dialog.getByRole("combobox", { name: "Network", exact: true });
  await expect(select).toHaveAttribute("data-value", "revoked");
  await chooseValue(select, network.id);
  await dialog.getByRole("button", { name: "Apply network change", exact: true }).click();
  await expect(select).toHaveAttribute("data-value", network.id);
  expect(networkBody).toMatchObject({ expected_revision: 1, network_template_id: network.id });
  await chooseValue(dialog.getByRole("combobox", { name: "Directory", exact: true }), "shared");
  await dialog.getByRole("button", { name: "notes.txt", exact: true }).click();
  const editor = dialog.getByRole("textbox", { name: "notes.txt", exact: true });
  await editor.fill("unsaved shared contents");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  const confirmation = page.getByRole("dialog", { name: "Discard unsaved file changes?", exact: true });
  await expect(confirmation).toBeVisible();
  await confirmation.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(editor).toHaveValue("unsaved shared contents");
  expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth + 1)).toBe(true);
  await page.screenshot({ path: info.outputPath("advanced-settings.png") });
  await dialog.getByRole("button", { name: "Close settings", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Discard unsaved account changes?", exact: true })).toBeVisible();
});

test("shared file listing never displays old rows under a newly selected mount", async ({ page }) => {
  await workspaceFixture(page);
  const ref = { id: "n", name: "Standard", enabled: true, sources: [] };
  await page.route("**/api/me/configuration", route => route.fulfill({ json: { networks: [ref], environments: [ref], shared_volumes: [], default_network: ref, default_environment: ref, selected_network: ref, selected_environment: ref, preferences: { revision: 1, network_template_id: "", environment_template_id: "", show_shared_volumes: true }, network_default_source: [], environment_sources: [] } }));
  await page.route("**/api/me/project-configurations", route => route.fulfill({ json: { projects: [{ project_id: "battery", title: "Battery", network_template_id: "n", network_name: "Standard", environment_name: "Standard", state: "active", revision: 1 }] } }));
  await page.route("**/api/projects/battery/mounts", route => route.fulfill({ json: { mounts: [{ key: "first", name: "First", access: "read_only" }, { key: "second", name: "Second", access: "read_only" }] } }));
  await page.route("**/api/projects/battery/files?*", async route => {
    const mount = new URL(route.request().url()).searchParams.get("mount");
    if (!mount) return route.fallback();
    if (mount === "second") return route.fulfill({ status: 503, json: { error: "Directory unavailable" } });
    return route.fulfill({ json: { entries: [{ name: "first-only.txt", kind: "file", size: 1 }] } });
  });
  await page.goto("/?project=battery&session=analysis");
  const dialog = await openSettings(page);
  await dialog.getByText("Advanced / Developer", { exact: true }).click();
  await dialog.getByText("Existing projects", { exact: true }).click();
  await chooseValue(dialog.getByRole("combobox", { name: "Project", exact: true }), "battery");
  await chooseValue(dialog.getByRole("combobox", { name: "Directory", exact: true }), "first");
  await expect(dialog.getByRole("button", { name: "first-only.txt", exact: true })).toBeVisible();
  await chooseValue(dialog.getByRole("combobox", { name: "Directory", exact: true }), "second");
  await expect(dialog.getByRole("button", { name: "first-only.txt", exact: true })).toHaveCount(0);
  await expect(dialog.getByText("Files are taking longer to load. Please try again in a moment.", { exact: false })).toBeVisible();
});

test("settings dropdown stays above the modal and Escape only closes the dropdown",async({page},info)=>{
 await workspaceFixture(page);await page.setViewportSize({width:390,height:844});await page.goto("/?project=battery&session=analysis");
 const dialog=await openSettings(page),theme=dialog.getByRole("combobox",{name:"Theme mode",exact:true});
 await theme.click();const list=page.getByRole("listbox",{name:"Theme mode",exact:true});await expect(list).toBeVisible();
 expect(await list.evaluate(node=>node.closest("dialog")?.open)).toBe(true);
 await page.screenshot({path:info.outputPath("settings-dropdown-mobile.png")});
 await page.keyboard.press("Escape");await expect(list).toHaveCount(0);await expect(dialog).toBeVisible();await expect(theme).toBeFocused();
 await theme.press("ArrowDown");await page.keyboard.press("End");await page.keyboard.press("Enter");await expect(theme).toHaveAttribute("data-value","dark");
 expect(await theme.evaluate(node=>getComputedStyle(node).textDecorationLine)).toBe("none");
});

test("environment update details are read-only and fetched only from advanced settings", async ({ page }) => {
  const fixture=await workspaceFixture(page);
  const network={id:"network",name:"Research network",enabled:true,description:"",sources:[]};
  const environment={...network,id:"environment",name:"Research environment"};
  await page.route("**/api/me/configuration",route=>route.fulfill({json:{networks:[network],environments:[environment],shared_volumes:[],preferences:{revision:1,network_template_id:"",environment_template_id:"",show_shared_volumes:false},default_network:network,default_environment:environment,selected_network:network,selected_environment:environment,network_default_source:[],environment_sources:[],needs_admin_resolution:false}}));
  await page.route("**/api/me/project-configurations",route=>route.fulfill({json:{projects:[{project_id:"battery",title:"Battery research",state:"active",suspended:false,revision:1,network_template_id:"network",network_name:network.name,environment_name:environment.name}]}}));
  let reads=0;
  await page.route("**/api/me/project-configurations/battery/environment",route=>{reads++;return route.fulfill({json:{state:"deferred",updated_at:"2026-10-13T00:00:00Z",current:[{key:"knowledge",destination:"/knowledge",source:{version:"v1"}}]}})});
  await page.goto("/?project=battery&session=analysis");
  await expect(page.getByText("This project keeps its current content until an update can be applied on a future start.",{exact:true})).toHaveCount(0);
  expect(reads).toBe(0);
  const dialog=await openSettings(page);
  expect(reads).toBe(0);
  await dialog.getByText("Advanced / Developer",{exact:true}).click();
  await dialog.getByText("Existing projects",{exact:true}).click();
  await chooseValue(dialog.getByRole("combobox",{name:"Project",exact:true}),"battery");
  expect(reads).toBe(0);
  await dialog.locator("summary").filter({hasText:/^Environment$/}).click();
  await expect(dialog.getByText("/knowledge · v1",{exact:true})).toBeVisible();
  expect(reads).toBe(1);
  await expect(dialog.getByRole("button",{name:/upgrade|rollback|publish|keep current versions/i})).toHaveCount(0);
  await dialog.getByRole("button",{name:"Close settings",exact:true}).click();
  await expect(page.getByText("/knowledge · v1",{exact:true})).toHaveCount(0);
  expect(fixture.calls.some(call=>call.method!=="GET"&&call.path.includes("environment"))).toBe(false);
});

test("settings disclosures keep dialog and header geometry stable", async ({ page }) => {
  await workspaceFixture(page);
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.goto("/?project=battery&session=analysis");
  const dialog = await openSettings(page);
  const header = dialog.locator(":scope > header");
  const frame = await dialog.boundingBox();
  const heading = await header.boundingBox();
  const toggle = dialog.locator("summary").filter({ hasText: /^Advanced \/ Developer$/ });
  await toggle.click();
  await expect(toggle.locator("..")).toHaveAttribute("open", "");
  expect(await dialog.boundingBox()).toEqual(frame);
  expect(await header.boundingBox()).toEqual(heading);
  await toggle.click();
  expect(await dialog.boundingBox()).toEqual(frame);
  expect(await header.boundingBox()).toEqual(heading);
});

test("account language overrides browser language and persists without closing settings", async ({ page }) => {
  await workspaceFixture(page);
  let saved: Preferences = { revision: 1, theme_mode: "system", accent: "neutral", language: "system" };
  await page.route("**/api/me/preferences", route => {
    if (route.request().method() === "PATCH") {
      const body = route.request().postDataJSON();
      saved = { ...saved, ...body, revision: saved.revision + 1 };
    }
    return route.fulfill({ json: saved });
  });
  await page.goto("/");
  const dialog = await openSettings(page);
  await chooseValue(dialog.getByRole("combobox", { name: "Language", exact: true }), "zh");
  await expect(page.getByRole("dialog", { name: "设置", exact: true })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
  expect(saved.language).toBe("zh");
  await page.goto("/?view=schedules");
  const scheduleStatus = page.locator('.schedule-row .schedule-status[data-state="active"]');
  await expect(scheduleStatus).toContainText("已启用");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-Hans");
  await page.getByRole("button", { name: "账户", exact: true }).click();
  await page.getByRole("menuitem", { name: "设置", exact: true }).click();
  await chooseValue(page.getByRole("combobox", { name: "语言", exact: true }), "en");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  expect(saved.language).toBe("en");
});
