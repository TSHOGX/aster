import {expect,test} from "@playwright/test";
import {workspaceFixture} from "./fixture";

test("an expired directory continuation rebuilds the observed window",async({page})=>{
 await workspaceFixture(page);
 let generation=0;
 await page.route("**/api/projects/battery/files?**",route=>{
  const url=new URL(route.request().url());
  if(url.searchParams.get("path"))return route.fallback();
  const cursor=url.searchParams.get("after");
  if(cursor==="expired")return route.fulfill({status:409,contentType:"application/json",body:JSON.stringify({code:"resync_required",error:"resource requires resync"})});
  if(!cursor)generation++;
  const start=cursor==="fresh"?100:0;
  const entries=Array.from({length:100},(_,n)=>({name:`entry-${String(start+n).padStart(3,"0")}.txt`,kind:"file",size:0}));
  return route.fulfill({contentType:"application/json",body:JSON.stringify({entries,...(!cursor?{next:generation===1?"expired":"fresh"}:{})})});
 });
 await page.goto("/?project=battery&session=analysis");
 await page.getByRole("button",{name:"File preview",exact:true}).click();
 await expect(page.getByRole("button",{name:"entry-099.txt",exact:true})).toBeVisible();
 await page.locator(".files-panel").getByRole("button",{name:"Show more",exact:true}).click();
 await expect(page.getByRole("button",{name:"entry-199.txt",exact:true})).toBeVisible();
 await expect(page.getByRole("button",{name:"entry-000.txt",exact:true})).toHaveCount(1);
 await expect(page.locator(".files-panel")).not.toContainText("resource requires resync");
});
