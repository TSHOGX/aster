import { test, expect } from "@playwright/test";
const origin = "https://127.0.0.1:18443";

test("composition pages pin heads and recover failed or denied continuations", async ({ page }, info) => {
  test.setTimeout(120000);
  const width = info.project.name === "firefox" ? 1440 : 390;
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
  // More than one real branch page; only two branches need committed changes.
  const branches: {id:string;head:string}[] = [];
  for (let index = 0; index < 201; index++) branches.push(await post("/branches", {name:`Source ${index}`,base_commit:main.commit_id}));
  branches.sort((a,b) => a.id.localeCompare(b.id));
  const selected = branches[0], later = branches.at(-1)!;
  for (let index = 0; index < 201; index++) await post(`/branches/${selected.id}/draft/text`, {expected_head:selected.head,expected_revision:index+1,path:`entry-${String(index).padStart(3,"0")}.txt`,text:`Content ${index}`});
  const selectedCommit = await post(`/branches/${selected.id}/commits`, {expected_head:selected.head,expected_revision:202,message:"Paged source"});
  await post(`/branches/${later.id}/draft/text`, {expected_head:later.head,expected_revision:1,path:"late.txt",text:"Later branch content"});
  await post(`/branches/${later.id}/commits`, {expected_head:later.head,expected_revision:2,message:"Later branch"});
  const reads: string[]=[];
  page.on("request", request => {if(request.url().includes("/head-diff?")) reads.push(request.url());});
  await page.goto(`/volumes-studio/libraries/${library.id}?branch=${selected.id}&view=review`);
  const review = page.getByRole("region", {name:"Integration review",exact:true});
  const composition = review.getByRole("region", {name:"Compose selected changes",exact:true});
  await expect(composition).toContainText("Showing 200 of 201 committed paths.");
  expect(reads).toHaveLength(1); // Catalog discovery does not fetch every branch tree.
  await composition.getByRole("button", {name:"Clear selected paths",exact:true}).click();
  await expect(composition.getByRole("checkbox", {checked:true})).toHaveCount(0);
  const continuation = `**${root}/branches/${selected.id}/head-diff?*`;
  await page.route(continuation, route => new URL(route.request().url()).searchParams.get("after") ? route.abort("failed") : route.continue());
  await composition.getByRole("button", {name:"More committed paths",exact:true}).click();
  await expect(composition).toHaveCount(0);
  await expect(review.getByRole("alert")).toBeVisible();
  await page.unroute(continuation);
  await page.getByRole("navigation", { name: "Library views", exact: true }).getByRole("button", { name: "Review", exact: true }).click();
  await review.getByRole("button", {name:"Reload integration review",exact:true}).click();
  await expect(composition).toContainText("Showing 200 of 201 committed paths.");
  await expect(composition.getByRole("checkbox", {checked:true})).toHaveCount(0);

  // A changed source invalidates its old page cursor rather than mixing heads.
  await post(`/branches/${selected.id}/draft/text`, {expected_head:selectedCommit.commit_id,expected_revision:202,path:"entry-201.txt",text:"Advanced source"});
  const advanced = await post(`/branches/${selected.id}/commits`, {expected_head:selectedCommit.commit_id,expected_revision:203,message:"Advance source"});
  const stale = page.waitForResponse(response => response.url().includes("/head-diff?") && response.url().includes("after="));
  await composition.getByRole("button", {name:"More committed paths",exact:true}).click();
  expect((await stale).status()).toBe(409); await expect(composition).toHaveCount(0);
  await page.getByRole("navigation", { name: "Library views", exact: true }).getByRole("button", { name: "Review", exact: true }).click();
  await review.getByRole("button", {name:"Reload integration review",exact:true}).click();
  await expect(composition).toContainText("Showing 200 of 202 committed paths.");
  await expect(composition).toContainText(advanced.commit_id);
  await composition.getByRole("button", {name:"Clear selected paths",exact:true}).click();
  await composition.getByRole("button", {name:"More committed paths",exact:true}).press("Enter");
  await expect(composition).toContainText("Showing 202 of 202 committed paths.");
  await expect(composition.getByRole("checkbox", {checked:true})).toHaveCount(0);
  await composition.getByRole("checkbox", {name:"entry-200.txt · added",exact:true}).check();
  await composition.getByRole("button", {name:"More source branches",exact:true}).press("Enter");
  // The branch's frozen head is displayed, while its paths load only on demand.
  const lateFieldset = composition.locator("fieldset").filter({has:page.getByRole("button",{name:"Load committed paths",exact:true})});
  await expect(lateFieldset).toHaveCount(1);
  await lateFieldset.getByRole("button",{name:"Load committed paths",exact:true}).press("Enter");
  await expect(composition.getByRole("checkbox",{name:"late.txt · added",exact:true})).not.toBeChecked();
  await composition.getByRole("checkbox",{name:"late.txt · added",exact:true}).check();
  await language("zh");
  await expect(page.getByRole("region",{name:"组合选定的更改",exact:true})).toContainText("202");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await page.screenshot({path:info.outputPath(`composition-pages-${width}.png`),fullPage:true});
  await language("en");
  await composition.getByRole("button",{name:"Prepare selected composition",exact:true}).click();
  await page.getByRole("navigation", { name: "Library views", exact: true }).getByRole("button", { name: "Review", exact: true }).click();
  await expect(review.getByRole("button",{name:"Approve integration",exact:true})).toBeEnabled();
  const digest = new URL(page.url()).searchParams.get("integration")!;
  const prepared = await get(`/integrations/${encodeURIComponent(digest)}`);
  expect(prepared.identity.selections.find((item:{branch_id:string})=>item.branch_id===selected.id).selected_paths).toEqual(["entry-200.txt"]);
  expect(prepared.identity.selections.find((item:{branch_id:string})=>item.branch_id===later.id).selected_paths).toEqual(["late.txt"]);
  const changed = await get(`/integrations/${encodeURIComponent(digest)}/changes`);
  expect(changed.changes.map((item:{path:string})=>item.path)).toEqual(["entry-200.txt","late.txt"]);

  await grant("volume.read","deny",1);
  await page.getByRole("navigation", { name: "Library views", exact: true }).getByRole("button", { name: "Review", exact: true }).click();
  await review.getByRole("button",{name:"Reload integration review",exact:true}).click();
  await expect(composition).toHaveCount(0);
  await expect(review.getByRole("list",{name:"Composition selection result",exact:true})).toHaveCount(0);
  await grant("volume.read","allow",2);
  await page.getByRole("navigation", { name: "Library views", exact: true }).getByRole("button", { name: "Review", exact: true }).click();
  await review.getByRole("button",{name:"Reload integration review",exact:true}).click();
  await expect(review.getByRole("list",{name:"Composition selection result",exact:true})).toContainText("entry-200.txt");
  expect(errors).toEqual([]);
});
