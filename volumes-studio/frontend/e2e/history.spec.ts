import {test,expect} from "@playwright/test";
const origin="https://127.0.0.1:18443";
for(const width of [390,1440])test(`immutable history and snapshot comparison at ${width}px`,async({page})=>{
 await page.setViewportSize({width,height:900});
 const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
 expect((await page.request.post(`${origin}/api/auth/login`,{headers:{Origin:origin},data:{username:"studio-admin",password:"browser-fixture-password"}})).ok()).toBeTruthy();
 await page.goto("/volumes-studio/");await page.getByRole("link",{name:"Sign in",exact:true}).click();
 const session=await(await page.request.get("/volumes-studio/api/v1/session")).json();
 const headers={Origin:origin,"X-CSRF-Token":session.csrf};
 const created=await page.request.post("/volumes-studio/api/v1/libraries",{headers,data:{request_id:crypto.randomUUID(),name:`History ${crypto.randomUUID()}`}});
 expect(created.ok()).toBeTruthy();const library=await created.json();
 // The creating administrator already owns the library.
 const root=`/volumes-studio/api/v1/libraries/${library.id}`;
 const post=async(path:string,data:object)=>{const response=await page.request.post(root+path,{headers,data});expect(response.ok(),await response.text()).toBeTruthy();return response.json();};
 const main=await(await page.request.get(root+"/main")).json();
 const branch=await post("/branches",{request_id:crypto.randomUUID(),name:"History candidate",base_commit:main.commit_id});
 await post(`/branches/${branch.id}/draft/text`,{request_id:crypto.randomUUID(),expected_head:branch.head,expected_revision:1,path:"note.md",text:"First immutable text"});
 const first=await post(`/branches/${branch.id}/commits`,{request_id:crypto.randomUUID(),expected_head:branch.head,expected_revision:2,message:"First checkpoint"});
 await post(`/branches/${branch.id}/draft/text`,{request_id:crypto.randomUUID(),expected_head:first.commit_id,expected_revision:2,path:"note.md",text:"Second immutable text"});
 const second=await post(`/branches/${branch.id}/commits`,{request_id:crypto.randomUUID(),expected_head:first.commit_id,expected_revision:3,message:"Second checkpoint"});
 let initialArrived!:()=>void, releaseInitial!:()=>void;
 const initialWaiting=new Promise<void>(resolve=>{initialArrived=resolve;}),initialHeld=new Promise<void>(resolve=>{releaseInitial=resolve;});
 await page.route(`**${root}/history?**`,async route=>{
  if(new URL(route.request().url()).searchParams.get("head")!==main.commit_id)return route.continue();
  const response=await route.fetch();initialArrived();await initialHeld;await route.fulfill({response}).catch(()=>{});
 });
 await page.goto(`/volumes-studio/libraries/${library.id}?branch=${branch.id}&view=history`);
 const history=page.getByRole("region",{name:"Commit history and comparison",exact:true});
 await initialWaiting;
 await expect(history.getByRole("button",{name:"Load branch head",exact:true})).toBeDisabled();
 releaseInitial();
 await history.getByRole("button",{name:"Load branch head",exact:true}).click();
 await page.unroute(`**${root}/history?**`);
 await expect(history.getByText("First checkpoint",{exact:true})).toBeVisible();
 await expect(history.getByText("Second checkpoint",{exact:true})).toBeVisible();
 await history.getByLabel("Base commit",{exact:true}).fill(first.commit_id);
 await history.getByLabel("Target commit",{exact:true}).fill(second.commit_id);
 await history.getByRole("button",{name:"Compare snapshots",exact:true}).click();
 await history.getByRole("button",{name:"note.md · changed",exact:true}).click();
 await expect(history.getByLabel("File differences")).toContainText("Second immutable text");
 const download=await page.request.get(await history.getByRole("link",{name:"Download base file",exact:true}).getAttribute("href")||"");
 expect(download.ok()).toBeTruthy();expect(await download.text()).toBe("First immutable text");
 expect(download.headers()["content-security-policy"]).toContain("sandbox");
 // Mutable branch work cannot change the selected immutable comparison.
 await post(`/branches/${branch.id}/draft/text`,{request_id:crypto.randomUUID(),expected_head:second.commit_id,expected_revision:3,path:"note.md",text:"Later mutable draft"});
 await page.reload();
 await expect(history.getByLabel("Base commit",{exact:true})).toHaveValue(first.commit_id);
 await expect(history.getByLabel("Target commit",{exact:true})).toHaveValue(second.commit_id);
 await history.getByRole("button",{name:"note.md · changed",exact:true}).click();
 await expect(history.getByLabel("File differences")).not.toContainText("Later mutable draft");
 const account=await(await page.request.get("/api/auth/session")).json();
 const language=async(value:string)=>{const saved=await(await page.request.get("/api/me/preferences")).json();expect((await page.request.patch("/api/me/preferences",{headers:{Origin:origin},data:{expected_revision:saved.revision,language:value}})).ok()).toBeTruthy();await page.evaluate(id=>window.dispatchEvent(new StorageEvent("storage",{key:`account-appearance:${id}`})),account.id);};
 try {
  await language("zh");await expect(page.getByRole("region",{name:"提交历史与比较",exact:true})).toBeVisible();
  await expect(page.getByLabel("基准提交",{exact:true})).toHaveValue(first.commit_id);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
 }finally{await language("en");}
 // Current authority failure removes loaded history, file metadata and content.
 const access=async(effect:string,revision:number)=>expect((await page.request.post("/api/admin/studio-grants",{headers:{Origin:origin},data:{request_id:crypto.randomUUID(),kind:"account",subject_id:session.subject,library_id:library.id,action:"volume.read",effect,expected_revision:revision}})).ok()).toBe(true);
 await access("deny",1);
 await history.getByRole("button",{name:"Compare snapshots",exact:true}).click();
 await expect(history.getByRole("alert")).toContainText("History is unavailable.");
 await expect(history.getByText("First checkpoint",{exact:true})).toHaveCount(0);
 await expect(history.getByRole("region",{name:"Snapshot file comparison",exact:true})).toHaveCount(0);
 expect((await page.request.get(`${root}/commits/${first.commit_id}/content?path=note.md`)).status()).toBe(403);
 await access("allow",2);
 await history.getByRole("button",{name:"Retry",exact:true}).click();
 await expect(history.getByText("First checkpoint",{exact:true})).toBeVisible();
 await history.getByRole("button",{name:"note.md · changed",exact:true}).click();
 await expect(history.getByLabel("File differences")).toContainText("Second immutable text");
 expect(errors).toEqual([]);
});
