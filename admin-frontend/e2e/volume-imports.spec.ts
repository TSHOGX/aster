import { test, expect } from '@playwright/test';
import { adminFixture } from './fixture';

for (const profile of ['authored-v1', 'shared-assets-v1']) test(`select exact ${profile} release, recover a lost admission reply and retain offline local availability`, async ({ page },info) => {
  await adminFixture(page);
  page.on('dialog',dialog=>dialog.accept());
  const digest = 'sha256:' + 'a'.repeat(64), tree = 'sha256:' + 'b'.repeat(64);
  const imported = { id: 'import-one', source_id: 'studio', publisher_id: 'publisher', library_id: 'library', version_id: 'version', manifest_digest: digest, state: 'acquiring', capacity_bytes: '67108864', transfer_reserved_bytes: '16777216', actor_id: 'admin-id', publisher_actor: 'https://identity/author', artifact: { id: 'studio-import-one', version: 'version', digest: tree } };
  let admitted = false, lost = false, sourceOffline = false;
  const requests: Record<string, string>[] = [];
  await page.route(/\/api\/admin\/volume-(sources|imports)(\/|\?|$)/, async route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/admin/volume-sources') return route.fulfill({ json: { sources: [{ id: 'studio', publisher_id: 'publisher' }] } });
    if (url.pathname.endsWith('/libraries')) return sourceOffline ? route.fulfill({ status: 503, json: { error: 'Source unavailable' } }) : route.fulfill({ json: { libraries: [{ id: 'library', name: 'Handbook', description: '' }], next: '' } });
    if (url.pathname.endsWith('/releases')) return route.fulfill({ json: { releases: [{ id: 'version', library_id: 'library', label: 'v1', profile, note: 'Reviewed handbook', descriptor_digest: digest, publisher: { issuer: 'https://identity', subject: 'author' } }], next: '' } });
    if (url.pathname === '/api/admin/volume-imports' && route.request().method() === 'POST') {
      requests.push(route.request().postDataJSON()); admitted = true;
      if (!lost) { lost = true; return route.abort('failed'); }
      return route.fulfill({ status: 202, json: imported });
    }
    if (url.pathname === '/api/admin/volume-imports/import-one') return route.fulfill({json:imported});
    if (url.pathname === '/api/admin/volume-imports') return route.fulfill({ json: { imports: admitted ? [imported] : [] } });
    return route.fallback();
  });
  await page.route('**/api/admin/runtime/options?*', route => route.fulfill({ json: { max_mounts: '8', options: [] } }));
  await page.goto('/admin/resources/volumes?volume_publisher=publisher&volume_library=library&volume_version=version');
  await expect(page.getByRole('link', { name: 'Open Volume Studio' })).toHaveAttribute('href', '/volumes-studio/');
  await expect(page.getByText('Reviewed handbook')).toBeVisible();
  await page.getByRole('button', { name: 'Import release', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Check original request' })).toBeVisible();
  const preferences={revision:2,theme_mode:'system',accent:'neutral',language:'zh'};
  await page.route('**/api/me/preferences',route=>route.fulfill({json:preferences}));
  await page.evaluate(()=>window.dispatchEvent(new StorageEvent('storage',{key:'account-appearance:admin-id'})));
  await expect(page.locator('html')).toHaveAttribute('lang','zh-Hans');
  await page.screenshot({path:info.outputPath(`import-recovery-${profile}.png`),fullPage:true});
  preferences.language='en';await page.evaluate(()=>window.dispatchEvent(new StorageEvent('storage',{key:'account-appearance:admin-id'})));
  await expect(page.locator('html')).toHaveAttribute('lang','en');
  await page.reload();
  await expect(page.getByRole('button', { name: 'Check original request' })).toBeEnabled();
  await page.getByRole('button', { name: 'Check original request' }).click();
  await expect(page.getByRole('button', { name: 'Check original request' })).not.toBeVisible();
  expect(requests).toHaveLength(2); expect(requests[0]).toEqual(requests[1]);
  expect(requests[0]).toMatchObject({ source_id: 'studio', version_id: 'version', expected_manifest_digest: digest, consumer_profile: profile });
  expect(requests[0]).not.toHaveProperty('endpoint');
  imported.state = 'ready'; imported.transfer_reserved_bytes = '0';
  await page.getByRole('button', { name: 'Refresh sources and imports' }).click();
  await expect(page.getByRole('link', { name: 'Use in Environment' })).toBeVisible();
  sourceOffline = true;
  await page.getByRole('button', { name: 'Refresh sources and imports' }).click();
  await expect(page.getByText(/Source unavailable. Locally ready volumes remain usable./)).toBeVisible();
  await expect(page.getByRole('link', { name: 'Use in Environment' })).toBeVisible();
});

for(const action of ['retry','cancel','retire'])test(`import ${action} preserves exact recovery through reload and denied reads`,async({page})=>{
 await adminFixture(page);page.on('dialog',dialog=>dialog.accept());
 await page.setViewportSize({width:390,height:1000});
 let denied=false,lost=false;
 const requests:string[]=[];
 const imported={id:'local-one',source_id:'studio',publisher_id:'publisher',library_id:'library',version_id:'version',manifest_digest:`sha256:${'a'.repeat(64)}`,state:action==='retry'?'failed':action==='retire'?'ready':'acquiring',capacity_bytes:'100',transfer_reserved_bytes:'0',actor_id:'admin-id',publisher_actor:'expert',artifact:{id:'artifact',version:'version',digest:'digest'}};
 await page.route(/\/api\/admin\/volume-(sources|imports)(\/|\?|$)/,async route=>{
  const url=new URL(route.request().url());
  if(url.pathname==='/api/admin/volume-sources')return route.fulfill({json:{sources:[]}});
  if(route.request().method()==='GET')return denied?route.fulfill({status:403,json:{error:'Denied',code:'forbidden'}}):route.fulfill({json:url.pathname.endsWith('/local-one')?imported:{imports:[imported]}});
  expect(url.pathname).toBe(`/api/admin/volume-imports/local-one/${action}`);requests.push(route.request().postData()!);
  imported.state=action==='retry'?'acquiring':action==='cancel'?'canceled':'retiring';
  if(!lost){lost=true;return route.abort('failed');}return route.fulfill({json:imported});
 });
 await page.goto('/admin/resources/volumes');
 const panel=page.getByRole('region',{name:'Published library volumes',exact:true});
 await panel.getByRole('button',{name:action==='retry'?'Retry as current admin':action==='cancel'?'Cancel':'Retire locally',exact:true}).click();
 if(action!=='retry')await page.getByRole('dialog').getByRole('button',{name:action==='cancel'?'Cancel import':'Retire locally',exact:true}).click();
 await expect(panel.getByRole('button',{name:'Check original request',exact:true})).toBeEnabled();
 denied=true;await page.reload();
 await expect(panel.getByRole('alert')).toContainText('Denied');
 await expect(panel.getByRole('button',{name:'Check original request',exact:true})).toHaveCount(0);
 await expect(panel.getByText('local-one',{exact:true})).toHaveCount(0);
 denied=false;await panel.getByRole('button',{name:'Refresh sources and imports',exact:true}).click();
 await expect(panel.getByRole('button',{name:'Check original request',exact:true})).toBeEnabled();
 await panel.getByRole('button',{name:'Check original request',exact:true}).press('Enter');
 await expect(panel.getByRole('button',{name:'Check original request',exact:true})).toHaveCount(0);
 expect(requests).toHaveLength(2);expect(requests[1]).toBe(requests[0]);
 expect(new URL(page.url()).searchParams.get('volume_import')).toBe('local-one');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
});

test('import pagination survives polling, clears unavailable evidence and restores a pinned selection',async({page})=>{
 await adminFixture(page);
 let failed=false,reads=0;
 const entry=(id:string)=>({id,source_id:'studio',publisher_id:'publisher',library_id:'library',version_id:`version-${id}`,manifest_digest:`sha256:${'a'.repeat(64)}`,state:'ready',capacity_bytes:'100',transfer_reserved_bytes:'0',actor_id:'admin-id',publisher_actor:'expert',artifact:{id:`artifact-${id}`,version:'version',digest:'digest'}});
 const first=Array.from({length:200},(_,index)=>entry(`import-${String(index).padStart(3,'0')}`)),last=entry('last');
 await page.route(/\/api\/admin\/volume-(sources|imports)(\/|\?|$)/,async route=>{
  const url=new URL(route.request().url());
  if(url.pathname==='/api/admin/volume-sources')return route.fulfill({json:{sources:[]}});
  if(failed)return route.fulfill({status:503,json:{error:'Local read unavailable'}});
  if(url.pathname.endsWith('/last'))return route.fulfill({json:last});
  reads++;return route.fulfill({json:{imports:url.searchParams.has('after')?[last]:first}});
 });
 await page.goto('/admin/resources/volumes');const panel=page.getByRole('region',{name:'Published library volumes',exact:true});
 await panel.getByRole('button',{name:'More imports',exact:true}).click();
 await expect(panel.getByText('version-last',{exact:true})).toBeVisible();
 const loadedReads=reads;await expect.poll(()=>reads,{timeout:6000}).toBeGreaterThan(loadedReads);
 await expect(panel.getByText('version-last',{exact:true})).toBeVisible();
 failed=true;await panel.getByRole('button',{name:'Refresh sources and imports',exact:true}).click();
 await expect(panel.getByRole('alert')).toContainText('Local read unavailable');await expect(panel.getByText('version-last',{exact:true})).toHaveCount(0);
 failed=false;await page.goto('/admin/resources/volumes?volume_import=last');await expect(panel.getByText('version-last',{exact:true})).toBeVisible();
});

test('malformed import recovery blocks mutations and failed storage preserves exact in-tab requests',async({page})=>{
 await adminFixture(page);page.on('dialog',dialog=>dialog.accept());
 const key='admin-volume-import:'+JSON.stringify('admin-id');
 await page.addInitScript(key=>sessionStorage.setItem(key,'{broken'),key);
 const digest=`sha256:${'a'.repeat(64)}`;const bodies:string[]=[];
 const imported={id:'memory-import',source_id:'studio',publisher_id:'publisher',library_id:'library',version_id:'version',manifest_digest:digest,state:'acquiring',capacity_bytes:'100',transfer_reserved_bytes:'0',actor_id:'admin-id',publisher_actor:'expert',artifact:{id:'artifact',version:'version',digest:'digest'}};
 let admitted=false;
 await page.route(/\/api\/admin\/volume-(sources|imports)(\/|\?|$)/,async route=>{
  const url=new URL(route.request().url());
  if(url.pathname==='/api/admin/volume-sources')return route.fulfill({json:{sources:[{id:'studio',publisher_id:'publisher'}]}});
  if(url.pathname.endsWith('/libraries'))return route.fulfill({json:{libraries:[{id:'library',name:'Library',description:''}],next:''}});
  if(url.pathname.endsWith('/releases'))return route.fulfill({json:{releases:[{id:'version',library_id:'library',label:'v1',note:'Exact release',descriptor_digest:digest,profile:'authored-v1',publisher:{issuer:'issuer',subject:'expert'}}],next:''}});
  if(route.request().method()==='GET')return route.fulfill({json:url.pathname.endsWith('/memory-import')?imported:{imports:admitted?[imported]:[]}});
  bodies.push(route.request().postData()!);admitted=true;
  if(bodies.length===1)return route.fulfill({status:202,json:{}}); // Incomplete success remains uncertain.
  return route.fulfill({json:imported});
 });
 await page.goto('/admin/resources/volumes');const panel=page.getByRole('region',{name:'Published library volumes',exact:true});
 await expect(panel).toContainText('Local import recovery is invalid.');await expect(panel.getByRole('button',{name:'Import release',exact:true})).toBeDisabled();
 await panel.getByRole('button',{name:'Discard import recovery',exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'Discard import recovery',exact:true}).click();await expect(panel.getByRole('button',{name:'Import release',exact:true})).toBeEnabled();
 await page.evaluate(key=>{const original=Storage.prototype.setItem;Storage.prototype.setItem=function(k,v){if(k===key)throw new Error('storage unavailable');return original.call(this,k,v);};},key);
 await panel.getByRole('button',{name:'Import release',exact:true}).click();await expect(panel.getByRole('button',{name:'Check original request',exact:true})).toBeEnabled();
 await expect(panel).toContainText('Local import recovery is unavailable.');
 await panel.getByRole('button',{name:'Check original request',exact:true}).press('Enter');
 await expect(panel.getByRole('button',{name:'Check original request',exact:true})).toHaveCount(0);expect(bodies).toHaveLength(2);expect(bodies[1]).toBe(bodies[0]);
});

test('failed catalog continuations clear their dependent release evidence',async({page})=>{
 await adminFixture(page);let failLibraries=false,failReleases=false,failSources=false;
 await page.route(/\/api\/admin\/volume-(sources|imports)(\/|\?|$)/,async route=>{
  const url=new URL(route.request().url());
  if(url.pathname==='/api/admin/volume-sources')return failSources?route.fulfill({status:503,json:{error:'Source discovery unavailable'}}):route.fulfill({json:{sources:[{id:'studio',publisher_id:'publisher'}]}});
  if(url.pathname.endsWith('/libraries'))return failLibraries&&url.searchParams.has('after')?route.fulfill({status:503,json:{error:'Library continuation unavailable'}}):route.fulfill({json:{libraries:[{id:'library',name:'Library',description:''}],next:'next-library'}});
  if(url.pathname.endsWith('/releases'))return failReleases&&url.searchParams.has('after')?route.fulfill({status:503,json:{error:'Release continuation unavailable'}}):route.fulfill({json:{releases:[{id:'version',library_id:'library',label:'v1',note:'Exact release',descriptor_digest:`sha256:${'a'.repeat(64)}`,profile:'authored-v1',publisher:{issuer:'issuer',subject:'expert'}}],next:'next-release'}});
  return route.fulfill({json:{imports:[]}});
 });
 await page.goto('/admin/resources/volumes');const panel=page.getByRole('region',{name:'Published library volumes',exact:true});
 await expect(panel.getByText('Exact release',{exact:true})).toBeVisible();
 failReleases=true;await panel.getByRole('button',{name:'More releases',exact:true}).click();await expect(panel.getByText('Exact release',{exact:true})).toHaveCount(0);await expect(panel.getByRole('button',{name:'More releases',exact:true})).toHaveCount(0);
 await panel.getByRole('button',{name:'Refresh sources and imports',exact:true}).click();await expect(panel.getByText('Exact release',{exact:true})).toBeVisible();
 failLibraries=true;await panel.getByRole('button',{name:'More libraries',exact:true}).click();await expect(panel.getByText('Exact release',{exact:true})).toHaveCount(0);await expect(panel.getByRole('button',{name:'More libraries',exact:true})).toHaveCount(0);
 failSources=true;await panel.getByRole('button',{name:'Refresh sources and imports',exact:true}).click();await expect(panel).toContainText('Source discovery unavailable');await expect(panel.getByRole('button',{name:'Import release',exact:true})).toHaveCount(0);
});
