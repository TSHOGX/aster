import { expect,test } from "@playwright/test";
import { spaceZH as ZH } from "../../evolution-space/frontend/lib/i18n-catalog";
test("ordinary operator starts exact manual batch with automation off",async({page:admin,browser,baseURL},info)=>{
 const tr=(text:string)=>process.env.ADMIN_EVOLUTION_LOCALE?.startsWith("zh")?ZH[text]||text:text;
 await admin.context().addCookies([{name:"project_login",value:process.env.ADMIN_EVOLUTION_TOKEN!,url:baseURL!,secure:true,httpOnly:true,sameSite:"Lax"}]);
 const created=await admin.request.post("/api/admin/users",{headers:{Origin:baseURL!},data:{request_id:crypto.randomUUID(),username:`operator-${crypto.randomUUID()}`,display_name:"Scoped operator",role:"user",group_ids:["standard-users"]}});
 expect(created.status(),await created.text()).toBe(201);const user=await created.json();
 for(const action of ["config.read","run.start","source.read"]){const response=await admin.request.put("/api/admin/groups/standard-users/optimization/grants",{headers:{Origin:baseURL!},data:{request_id:crypto.randomUUID(),account_id:user.user.id,action,effect:"allow",expected_revision:0}});expect(response.ok(),await response.text()).toBe(true)}
 expect((await admin.request.post("/api/admin/studio-grants",{headers:{Origin:baseURL!},data:{request_id:crypto.randomUUID(),kind:"account",subject_id:user.user.id,library_id:"aster-shared-assets",action:"volume.read",effect:"allow",expected_revision:0}})).ok()).toBe(true);
 const context=await browser.newContext({baseURL,ignoreHTTPSErrors:true,locale:process.env.ADMIN_EVOLUTION_LOCALE||"en-US",viewport:{width:Number(process.env.ADMIN_EVOLUTION_WIDTH||390),height:1000}});
 try{
  const page=await context.newPage();const errors:string[]=[];page.on("pageerror",error=>errors.push(error.message));page.on("dialog",dialog=>dialog.accept());
  expect((await page.request.post("/api/auth/login",{headers:{Origin:baseURL!},data:{username:user.user.username,password:user.temporary_password}})).ok()).toBe(true);
  expect((await page.request.post("/api/auth/password/setup",{headers:{Origin:baseURL!},data:{new_password:"permanent-operator-password"}})).ok()).toBe(true);
  let lose=true;const requests:string[]=[];
  await page.route("**/evolution/manual",async route=>{requests.push(route.request().postData()!);if(lose){lose=false;const response=await route.fetch();expect(response.status(),await response.text()).toBe(202);await response.dispose();return route.abort("failed")}return route.continue()});
  await page.goto(`/evolution-space/auth/login?return=${encodeURIComponent("/evolution-space/groups/standard-users/runs/new/")}`);
  await expect(page.getByRole("heading",{name:tr("New run"),exact:true,level:1})).toBeVisible();
  for(const id of ["example-skills","example-corrections"]){await page.getByRole("checkbox",{name:new RegExp(`^${id} ·`)}).check();await page.getByLabel(`${id}: ${tr("Maximum tokens")}`,{exact:true}).fill("200");await page.getByLabel(`${id}: ${tr("Maximum cost (micros)")}`,{exact:true}).fill("2000")}
  await expect(page.getByRole("button",{name:tr("Start manual batch"),exact:true})).toBeDisabled();
  await page.getByRole("checkbox",{name:tr("I reviewed the selected workflows, input scope and spending ceilings."),exact:true}).check();
  await page.getByRole("button",{name:tr("Start manual batch"),exact:true}).press("Enter");
  await expect(page.getByRole("button",{name:tr("Check original request"),exact:true})).toBeVisible();
  await page.reload();await page.getByRole("button",{name:tr("Check original request"),exact:true}).press("Enter");
  await expect(page.getByRole("heading",{name:tr("Manual batch queued"),exact:true})).toBeVisible();
  expect(requests).toHaveLength(2);expect(requests[0]).toBe(requests[1]);
  expect((await page.request.get("/api/admin/users")).status()).toBe(403);
  const state=await(await page.request.get("/api/admin/groups/standard-users/optimization")).json();expect(state.auto_enabled).toBe(false);
  const work=await(await page.request.get("/api/admin/groups/standard-users/evolution/triggers")).json();expect(work.items.filter((item:{kind:string})=>item.kind==="manual")).toHaveLength(2);
  await page.screenshot({path:info.outputPath("manual-batch.png"),fullPage:true});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);expect(errors).toEqual([]);
 }finally{await context.close()}
});
