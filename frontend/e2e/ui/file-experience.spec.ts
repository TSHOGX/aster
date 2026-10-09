import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

for (const [path, body] of [
  ["report.md", "# Preview heading\n\n**A useful result**"],
  ["data.csv", 'name,value\n"Sample, A",42\nB,3'],
  ["data.tsv", 'name\tvalue\nSample A\t42'],
  ["page.html", '<html><head><style>h1{color:rgb(0,100,80)}</style></head><body><h1>HTML result</h1><script>document.querySelector("h1").textContent="Interactive HTML result";try{parent.document.body.dataset.compromised="yes"}catch{};fetch("/api/projects").catch(()=>{});</script></body></html>'],
  ["data.json", '{"cycles":42,"valid":true}'],
] as const) {
  test(`preview/source keeps one content read and a usable editor: ${path}`, async ({page}) => {
    const f = await workspaceFixture(page);
    f.messages.analysis[1].content = `[Artifact](/workspace/${path})`;
    f.files[path] = body;
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link", {name:"Artifact",exact:true}).click();
    const group = page.getByRole("group", {name:"File view"});
    await expect(group.getByRole("button", {name:"Preview",exact:true})).toHaveAttribute("aria-pressed","true");
    if (path.endsWith("html")) {
      await expect(page.frameLocator(".html-preview").getByRole("heading")).toHaveText("Interactive HTML result");
      expect(await page.locator("body").getAttribute("data-compromised")).toBeNull();
      await expect(page.locator(".html-preview")).toHaveAttribute("sandbox","allow-scripts");
    } else if (path.endsWith("csv") || path.endsWith("tsv")) {
      await expect(page.locator(".delimited-preview table")).toContainText("42");
    } else if (path.endsWith("md")) await expect(page.locator(".file-markdown h1")).toHaveText("Preview heading");
    await page.waitForTimeout(300);
    const reads = f.calls.filter(c=>c.path.endsWith("/files/content") && c.method==="GET").length;
    const source = group.getByRole("button", {name:"Source",exact:true});
    await source.focus(); await page.keyboard.press("Space");
    await expect(source).toHaveAttribute("aria-pressed","true");
    await expect(page.locator(".source-editor .cm-content")).toContainText(body.split("\n")[0]);
    await group.getByRole("button",{name:"Preview",exact:true}).click();
    expect(f.calls.filter(c=>c.path.endsWith("/files/content") && c.method==="GET")).toHaveLength(reads);
    await page.getByLabel("File actions", {exact:true}).click();
    await page.getByRole("menuitem",{name:"Edit text",exact:true}).click();
    await expect(page.getByLabel(`Edit ${path}`,{exact:true})).toBeVisible();
    await expect(group).toHaveCount(0);
  });
}

for (const [path, body] of [["data.bin",Buffer.from("binary")],["invalid.txt",Buffer.from([0,255,1])],["large.txt",Buffer.alloc((1<<20)+1,65)]] as const)
  test(`no misleading edit action: ${path}`, async ({page}) => {
    const f=await workspaceFixture(page); f.messages.analysis[1].content=`[Artifact](/workspace/${path})`;
    await page.route("**/files/content?*", route=>route.fulfill({body}));
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link",{name:"Artifact",exact:true}).click();
    await expect(page.getByText("Loading preview…",{exact:true})).toHaveCount(0);
    await page.getByLabel("File actions",{exact:true}).click();
    await expect(page.getByRole("menuitem",{name:"Download",exact:true})).toBeVisible();
    await expect(page.getByRole("menuitem",{name:"Edit text",exact:true})).toHaveCount(0);
    await expect(page.getByRole("group",{name:"File view"})).toHaveCount(0);
  });

test("HTML above the edit limit keeps rendered and source modes without another download", async ({ page }) => {
  const f = await workspaceFixture(page);
  f.messages.analysis[1].content = "[Artifact](/workspace/large.html)";
  const body = `<html><body><h1>Large page</h1><!--${"x".repeat(1 << 20)}--></body></html>`;
  await page.route("**/files/content?*", route => route.fulfill({ body }));
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link", { name: "Artifact", exact: true }).click();
  await expect(page.frameLocator(".html-preview").getByRole("heading", { name: "Large page" })).toBeVisible();
  const group = page.getByRole("group", { name: "File view" });
  await expect(group).toBeVisible();
  const reads = f.calls.filter(call => call.path.endsWith("/files/content") && call.method === "GET").length;
  await group.getByRole("button", { name: "Source", exact: true }).click();
  await expect(page.locator(".source-editor .cm-content")).toContainText("Large page");
  await group.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(page.frameLocator(".html-preview").getByRole("heading", { name: "Large page" })).toBeVisible();
  expect(f.calls.filter(call => call.path.endsWith("/files/content") && call.method === "GET")).toHaveLength(reads);
  await page.getByLabel("File actions", { exact: true }).click();
  await expect(page.getByRole("menuitem", { name: "Edit text", exact: true })).toHaveCount(0);
});

test("tree and preview recover from temporary capacity without an error notice", async ({page}) => {
  await workspaceFixture(page);
  let directoryAttempts=0, contentAttempts=0;
  await page.route("**/api/projects/battery/files?*", route=>
    ++directoryAttempts<=2 ? route.fulfill({status:503,json:{error:"capacity exhausted"}}) : route.fallback());
  await page.route("**/files/content?*", route=>
    ++contentAttempts<=2 ? route.fulfill({status:503,json:{error:"capacity exhausted"}}) : route.fallback());
  await page.goto("/?project=battery&session=analysis");
  await page.getByRole("link",{name:"the research report"}).click();
  await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
  await expect(page.locator('.file-row[data-file-path="reports/summary.md"]')).toBeVisible();
  await expect(page.getByText(/capacity exhausted/)).toHaveCount(0);
  await expect(page.locator(".files-panel .notice.error")).toHaveCount(0);
  expect(directoryAttempts).toBeGreaterThan(2); expect(contentAttempts).toBeGreaterThan(2);
});

for (const width of [390,1600]) for (const theme of ["light","dark"] as const)
  test(`representation controls fit ${width} ${theme}`, async ({page}) => {
    await page.setViewportSize({width,height:900}); await page.emulateMedia({colorScheme:theme});
    await workspaceFixture(page);
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link",{name:"the research report"}).click();
    const toggle=page.getByRole("group",{name:"File view"});
    await expect(toggle).toBeVisible();
    for (const name of ["Preview","Source"]) {
      const button=toggle.getByRole("button",{name,exact:true});
      await expect(button).toBeInViewport();
      await button.click();
      await expect(button).toHaveAttribute("aria-pressed","true");
    }
    await page.waitForTimeout(150);
    await page.screenshot({path:test.info().outputPath("file-view.png")});
  });
