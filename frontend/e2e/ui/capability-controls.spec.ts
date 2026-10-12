import {expect,test} from "@playwright/test";
import {workspaceFixture} from "./fixture";

test("denied work controls retain cleanup and drafts",async({page})=>{
 const fixture=await workspaceFixture(page,{deniedActions:["projects.create","projects.manage","agents.run","terminals.use"]});
 await page.goto("/?project=battery&session=analysis");
 await expect(page.getByLabel("New Project",{exact:true})).toBeDisabled();
 await expect(page.getByLabel("New chat in Battery research",{exact:true})).toBeDisabled();
 await page.getByRole("textbox",{name:"Message Compare cycle performance",exact:true}).fill("Retained draft");
 await expect(page.getByRole("button",{name:"Send",exact:true})).toBeDisabled();
 await page.getByRole("textbox",{name:"Message Compare cycle performance",exact:true}).press("Enter");
 expect(fixture.calls.filter(call=>call.method==="POST"&&call.path.endsWith("/turns"))).toEqual([]);
 await expect(page.getByRole("textbox",{name:"Message Compare cycle performance",exact:true})).toHaveValue("Retained draft");
 await page.getByLabel("Project actions Battery research",{exact:true}).click();
 await expect(page.getByRole("menuitem",{name:"Rename",exact:true})).toBeDisabled();
 await expect(page.getByRole("menuitem",{name:"Restart",exact:true})).toBeDisabled();
 await expect(page.getByRole("menuitem",{name:"Pause",exact:true})).toBeEnabled();
 await expect(page.getByRole("menuitem",{name:"Delete Project",exact:true})).toBeEnabled();
 await page.keyboard.press("Escape");
 await page.getByLabel("Toggle Terminal",{exact:true}).click();
 await expect(page.getByLabel("New terminal",{exact:true})).toBeDisabled();
 expect(fixture.terminals).toHaveLength(0);
});
