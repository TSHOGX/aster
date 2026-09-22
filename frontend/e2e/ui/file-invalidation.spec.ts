import {expect,test} from "@playwright/test";
import {workspaceFixture} from "./fixture";
test("unrelated writes skip the selected body; rotations preserve representation and edits",async({page})=>{
 const f=await workspaceFixture(page);
 await page.addInitScript(()=>{
   let urls=0;
   const create=URL.createObjectURL.bind(URL);
   URL.createObjectURL=blob=>{urls++;return create(blob);};
   Object.defineProperty(window,"fixtureURLs",{get:()=>urls});
 });
 const urls=()=>page.evaluate(()=>Reflect.get(window,"fixtureURLs") as number);
 const emit=(data:object|string)=>page.evaluate(data=>window.dispatchEvent(new CustomEvent("fixture-file-event",{detail:data})),data);
 await page.goto("/?project=battery&session=analysis");
 await page.getByRole("link",{name:"the research report"}).click();
 await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
 const count=await urls();
 const reads=()=>f.calls.filter(c=>c.path.endsWith("/files/content")&&c.method==="GET").length;
 const initial=reads();
 for(let i=0;i<100;i++) await emit({path:`other/${i}.md`,operation:"write",entry_type:"file"});
 await page.waitForTimeout(300);
 expect(reads()).toBe(initial);
 for(let i=0;i<2;i++) {
   await emit({reset:true,reason:"attachment"});
   await expect.poll(reads).toBe(initial+i+1);
   await page.waitForTimeout(150);
   expect(await urls()).toBe(count);
 }
 await page.getByLabel("File actions",{exact:true}).click();
 await page.getByRole("menuitem",{name:"Edit text",exact:true}).click();
 await page.getByLabel("Edit reports/summary.md",{exact:true}).fill("local unsaved draft");
 await emit("connection-error");
 await emit({reset:true,reason:"attachment"});
 await page.waitForTimeout(300);
 await expect(page.getByText("Files changed. Your unsaved text has been kept.",{exact:true})).toHaveCount(0);
 f.files["reports/summary.md"]=f.files["reports/summary.md"].replace("Cycle","Other");
 await emit({path:"reports/summary.md",operation:"write",entry_type:"file"});
 await expect(page.getByText("Files changed. Your unsaved text has been kept.",{exact:true})).toBeVisible();
 await expect(page.getByLabel("Edit reports/summary.md",{exact:true})).toContainText("local unsaved draft");
});

test("repeated reconnect failures do not postpone delayed-watch health",async({page})=>{
 await workspaceFixture(page);
 await page.clock.install();
 await page.goto("/?project=battery&session=analysis");
 await page.getByRole("link",{name:"the research report"}).click();
 await expect(page.locator(".file-markdown")).toContainText("Cycle performance report");
 await page.clock.pauseAt(new Date(Date.now()+1000));
 for(let i=0;i<3;i++) {
   await page.evaluate(()=>window.dispatchEvent(new CustomEvent("fixture-file-event",{detail:"connection-error"})));
   await page.clock.runFor(3000);
 }
 await expect(page.getByText("File updates may be delayed. Refresh to check.",{exact:true})).toBeVisible();
 await expect(page.getByText("Files changed. Your unsaved text has been kept.",{exact:true})).toHaveCount(0);
});
