import { expect, test } from "@playwright/test";
import { workspaceFixture } from "./fixture";

for (const width of [390,1600]) for (const kind of ["docx","xlsx"] as const) {
  test(`continuous ${kind} corpus is complete and bounded at ${width}px`,async({page})=>{
    await page.setViewportSize({width,height:1000});
    const fixture=await workspaceFixture(page,{projectState:"paused"});
    fixture.messages.analysis[1].content=`[Document](/workspace/corpus.${kind})`;
    const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
    await page.goto("/?project=battery&session=analysis");
    await page.getByRole("link",{name:"Document",exact:true}).click();
    const reader=page.locator(".office-reading-preview");
    await expect(reader).toHaveAttribute("data-preview-phase","ready");
    const final=kind==="docx"?3599:22962;
    const text=kind==="docx"?"Row 2999 cell 4":"Cell 22962:11";
    await page.getByLabel("Go to record",{exact:true}).fill(String(final));
    await page.getByRole("button",{name:"Go",exact:true}).click();
    if(kind==="xlsx")await page.locator(".office-reading-viewport").evaluate(n=>{n.scrollLeft=1500;});
    const last=reader.getByRole("button",{name:new RegExp(text+"$")});
    await expect(last).toBeVisible();await last.click();
    await expect(page.getByLabel("Selected text",{exact:true})).toHaveValue(text);
    expect(await reader.locator(".office-reading-row").count()).toBeLessThan(30);
    expect(await reader.locator("*").count()).toBeLessThan(500);
    await page.getByLabel("Find in file",{exact:true}).fill(kind==="docx"?"Paragraph 500":"Cell 500:0");
    await page.getByRole("button",{name:"Find next",exact:true}).click();
    if(kind==="xlsx")await page.locator(".office-reading-viewport").evaluate(n=>{n.scrollLeft=0;});
    await expect(reader).toContainText(kind==="docx"?"Paragraph 500":"Cell 500:0");
    if(width===1600){
      await page.getByText("Copy range",{exact:true}).first().click();
      await page.getByLabel("From record",{exact:true}).fill("99");await page.getByLabel("To record",{exact:true}).fill("102");
      if(kind==="xlsx"){await page.getByLabel("From column",{exact:true}).fill("1");await page.getByLabel("To column",{exact:true}).fill("2");}
      await page.getByRole("button",{name:"Copy range",exact:true}).click();
      await expect(page.getByLabel("Selected text",{exact:true})).toHaveValue(new RegExp(kind==="docx"?"Paragraph 101":"Cell 102:1"));
    }
    await page.locator(".agent-panel textarea").fill("Chat remains available");
    expect(fixture.calls.some(c=>c.path.endsWith("/resume"))).toBe(false);
    expect(await reader.evaluate(n=>n.scrollWidth<=n.clientWidth)).toBe(true);
    await page.screenshot({path:test.info().outputPath(`${kind}-${width}.png`),fullPage:true});
    if(width===390)await page.getByLabel("Toggle file tree",{exact:true}).click();
    await page.getByRole("button",{name:"README.md",exact:true}).click();
    await expect(page.locator(".file-markdown")).toContainText("Battery research");
    await expect.poll(()=>fixture.calls.filter(c=>c.method==="DELETE" && c.path.includes("/files/previews/")).length)
      .toBe(fixture.calls.filter(c=>c.method==="GET" && c.path.endsWith("/files/previews")).length);
    expect(errors).toEqual([]);
  });
}
