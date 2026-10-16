// Actual nested map/subgraph instances, exact parent attempts and ordered outputs.
import { chromium, firefox, webkit, expect } from '../../frontend/node_modules/@playwright/test/index.mjs';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
let input = ''; for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
const browser = await ({ chromium, firefox, webkit }[config.browser]).launch({ headless: true });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: config.width, height: 900 } });
try {
  await context.addCookies([{ name: 'project_login', value: config.cookie, url: config.origin, httpOnly: true, secure: true, sameSite: 'Lax' }]);
  const page = await context.newPage(); const errors = []; page.on('pageerror', error => errors.push(error.message));
  const root = `${config.origin}/evaluation-space/api/v1/groups/${config.group}/runs/${config.run}`;
  await page.goto(`${config.origin}/evaluation-space/groups/${config.group}/runs/${config.run}`);
  await page.getByRole('link', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: `Run inspector: ${config.run}`, exact: true })).toBeVisible();
  const records = (await (await page.request.get(`${root}/attempts?limit=100`)).json()).items;
  const leaves = records.filter(record => record.call.node.id === 'review-item').sort((a,b) => a.call.instance.localeCompare(b.call.instance));
  expect(leaves).toHaveLength(2);
  const map = records.find(record => record.call.node.kind === 'map');
  expect(map.outputs.items.map(item => item.id)).toEqual(['verified', 'followup']);
  const row = (details, label) => details.locator('.attempt-details > div').filter({ has: page.getByText(label, { exact: true }) });
  for (const [index, leaf] of leaves.entries()) {
    const summary = page.locator(`#attempt-${leaf.call.id} > summary`);
    await summary.focus(); await summary.press('Enter');
    const selected = page.locator(`#attempt-${leaf.call.id}`);
    await expect(selected).toHaveAttribute('open', '');
    await expect(row(selected, 'Map item').locator('dd')).toHaveText(String(index));
    await expect(row(selected, 'Execution depth').locator('dd')).toHaveText('2');
    await expect(row(selected, 'Inputs')).toContainText(index === 0 ? 'verified' : 'followup');
    await expect(row(selected, 'Effective prompt')).toContainText('Review this exact proposal');
    const parent = records.find(record => record.call.id === leaf.call.parent);
    expect(parent.call.node.kind).toBe('subgraph'); expect(parent.call.parent).toBe(map.call.id);
    const up = row(selected, 'Parent attempt').getByRole('button');
    await up.focus(); await up.press('Enter');
    await expect(page.locator(`#attempt-${parent.call.id}`)).toHaveAttribute('open', '');
    await page.goBack(); await expect(selected).toHaveAttribute('open', '');
    await page.reload(); await expect(selected).toHaveAttribute('open', '');
  }
  const account = await (await page.request.get(`${config.origin}/api/auth/session`)).json();
  const preference = await (await page.request.get(`${config.origin}/api/me/preferences`)).json();
  expect((await page.request.patch(`${config.origin}/api/me/preferences`, { headers: { Origin: config.origin }, data: { expected_revision: preference.revision, language: 'zh' } })).ok()).toBe(true);
  await page.evaluate(id => window.dispatchEvent(new StorageEvent('storage', { key: `account-appearance:${id}` })), account.id);
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hans');
  const selected = page.locator(`#attempt-${leaves[1].call.id}`);
  await expect(row(selected, '映射项目').locator('dd')).toHaveText('1');
  await page.reload(); await expect(selected).toHaveAttribute('open', '');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  mkdirSync(config.evidence_directory, { recursive: true });
  await page.screenshot({ path: path.join(config.evidence_directory, `nested-map-${config.width}.png`), fullPage: true });
  expect(errors).toEqual([]);
  process.stdout.write(`Native map/subgraph inspection passed (${config.browser}, ${config.width}px).\n`);
} finally { await context.close(); await browser.close(); }
