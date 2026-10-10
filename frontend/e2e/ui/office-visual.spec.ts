import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { workspaceFixture } from './fixture';

for (const width of [390, 1600]) for (const name of ['small.docx','small.xlsx','small.pptx','corpus.docx','corpus.xlsx','200-slides.pptx','charts.xlsx']) {
  test(`visual Office ${name} at ${width}`, async ({page}) => {
    test.setTimeout(180000);
    await page.setViewportSize({width,height:1000});
    const f=await workspaceFixture(page,{projectState:'paused'});
    f.messages.analysis[1].content=`[Open document](/workspace/${name})`;
    const bytes=readFileSync(resolve(process.env.PREVIEW_FIXTURES!,name));
    await page.route('**/files/content?*',route=>route.fulfill({body:bytes}));
    const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto('/?project=battery&session=analysis');
    await page.getByRole('link',{name:'Open document',exact:true}).click();
    await expect(page.locator('.office-preview')).toHaveAttribute('data-preview-phase',/ready|failed/,{timeout:120000});
    await expect(page.locator('.office-preview')).toHaveAttribute('data-preview-phase','ready',{timeout:1000});
    const frame=page.frameLocator('.office-frame');
    expect(await frame.locator('#content').evaluate(()=>{try{void parent.document;return false;}catch{return true;}})).toBe(true);
    if(name.endsWith('docx')) {
      await expect(frame.locator('section.docx').first()).toBeVisible();
      const pageFits = await frame.locator('section.docx').first().evaluate(n => {
        const page=n.getBoundingClientRect(); return page.left >= -1 && page.right <= window.innerWidth + 1;
      });
      expect(pageFits).toBe(true);
      await expect(frame.locator('#stage')).toContainText(name==='small.docx'?'Paragraph 0':'Row 2999 cell 4');
      if(name==='corpus.docx') await frame.getByText('Row 2999 cell 4',{exact:true}).scrollIntoViewIfNeeded();
    } else if(name.endsWith('xlsx')) {
      await expect(frame.locator('canvas').first()).toBeVisible();
      await expect(frame.locator('#stage')).toContainText(name==='corpus.xlsx'?'Measurements':'Sheet');
    } else {
      await expect(frame.locator('#content')).toContainText('Measured slide 0');
      if(name==='200-slides.pptx') {
        await frame.getByRole('spinbutton',{name:'Slide number'}).fill('200');
        await frame.getByRole('button',{name:'Go to slide'}).click();
        await expect(frame.locator('#content')).toContainText('Measured slide 199');
      }
    }
    await page.screenshot({path:test.info().outputPath(`${name}-${width}.png`),fullPage:true});
    await page.locator('.agent-panel textarea').fill('Still available');
    expect(f.calls.some(c=>c.path.includes('/files/previews')||c.path.endsWith('/resume'))).toBe(false);
    expect(errors).toEqual([]);
  });
}

test('revocation removes the visual document and mode does not depend on tree metadata',async({page})=>{
 const f=await workspaceFixture(page);f.messages.analysis[1].content='[Open document](/workspace/small.docx)';
 let denied=false;
 await page.route('**/files/content?*',r=>r.fulfill(denied?{status:404,json:{error:'Not found'}}:{body:readFileSync(resolve(process.env.PREVIEW_FIXTURES!,'small.docx'))}));
 await page.goto('/?project=battery&session=analysis');await page.getByRole('link',{name:'Open document',exact:true}).click();
 await expect(page.locator('.office-preview')).toHaveAttribute('data-preview-phase','ready');
 denied=true;await page.getByLabel('File actions',{exact:true}).click();await page.getByRole('menuitem',{name:'Refresh',exact:true}).click();
 await expect(page.locator('.office-frame')).toHaveCount(0);
});

test('viewer uses one classic script and reports a failed engine load before timeout', async ({page}) => {
  const f=await workspaceFixture(page);f.messages.analysis[1].content='[Open document](/workspace/small.docx)';
  await page.route('**/files/content?*',r=>r.fulfill({body:readFileSync(resolve(process.env.PREVIEW_FIXTURES!,'small.docx'))}));
  let fail=true;
  const scripts:string[]=[];
  page.on('request',r=>{if(r.url().includes('/office/')&&r.resourceType()==='script')scripts.push(r.url());});
  await page.route('**/office/*/document.js',r=>fail?r.abort():r.continue());
  await page.goto('/?project=battery&session=analysis');
  await page.getByRole('link',{name:'Open document',exact:true}).click();
  await expect(page.getByText('The Office viewer could not load. Retry or download the file.')).toBeVisible({timeout:10000});
  fail=false;
  await page.getByRole('button',{name:'Retry',exact:true}).click();
  await expect(page.locator('.office-preview')).toHaveAttribute('data-preview-phase','ready');
  const frame=page.frameLocator('.office-frame');
  await expect(frame.locator('script[type=module]')).toHaveCount(0);
  await expect(frame.locator('script[src]')).toHaveCount(1);
  expect(scripts.every(url=>url.endsWith('/document.js'))).toBe(true);
  // Firefox may retry an aborted transport request; only one engine URL is used.
  expect(new Set(scripts).size).toBe(1);
  expect(scripts.length).toBeGreaterThanOrEqual(2);
});
