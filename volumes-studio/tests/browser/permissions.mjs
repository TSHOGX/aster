// Actual administrator grants against central authority, before Studio admission.
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { chromium, firefox, webkit, expect } from '../../frontend/node_modules/@playwright/test/index.mjs';
let input = '';
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input), engine = config.browser || 'chromium';
const browser = await ({ chromium, firefox, webkit }[engine]).launch({ headless: true });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: config.width, height: 1000 } });
try {
  await context.addCookies([{ name: 'project_login', value: config.token, url: config.origin, secure: true, httpOnly: true, sameSite: 'Lax' }]);
  const page = await context.newPage(), errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('dialog', dialog => dialog.accept());
  await page.goto(`${config.origin}/admin/users/${config.account}/studio-permissions?library=aster-shared-assets`);
  const root = `${config.origin}/api/admin/studio-grants`;
  const inspect = async () => {
    const response = await page.request.get(`${root}?kind=account&subject_id=${config.account}&library_id=aster-shared-assets`);
    expect(response.status()).toBe(200); return response.json();
  };
  expect((await inspect()).grants.every(grant => grant.effect === 'unset' && !grant.allowed)).toBe(true);
  let lost = false;
  const writes = [];
  await page.route(root, async route => {
    if (route.request().method() !== 'POST') return route.continue();
    writes.push(route.request().postData());
    if (!lost) {
      const response = await route.fetch(); expect(response.status()).toBe(200); lost = true;
      return route.abort('failed');
    }
    return route.continue();
  });
  for (const [index, label] of ['Read Studio Volume', 'Author branches and drafts', 'Review integrations', 'Merge approved integrations', 'Publish and manage versions', 'Configure Volume policies'].entries()) {
    const row = page.getByRole('row').filter({ hasText: label });
    await row.getByRole('button', { name: 'Allow', exact: true }).press('Enter');
    await expect(page.getByRole('region', { name: 'Review permission change', exact: true })).toContainText(label);
    await page.getByRole('button', { name: 'Apply reviewed permission', exact: true }).press('Enter');
    if (index === 0) {
      await expect(page.getByRole('button', { name: 'Check original request', exact: true })).toBeVisible();
      await page.reload();
      await expect(page.getByLabel('Studio Volume ID', { exact: true })).toBeDisabled();
      await page.getByRole('button', { name: 'Check original request', exact: true }).click();
      expect(writes).toHaveLength(2); expect(writes[0]).toBe(writes[1]);
    }
    await expect(row.getByRole('button', { name: 'Allow', exact: true })).toBeDisabled();
    await expect(row).toContainText('Allowed');
  }
  expect((await inspect()).grants.every(grant => grant.effect === 'allow' && grant.allowed && grant.revision === 1)).toBe(true);
  await page.unroute(root);
  const language = async value => {
    const preference = await (await page.request.get(`${config.origin}/api/me/preferences`)).json();
    const response = await page.request.patch(`${config.origin}/api/me/preferences`, { headers: { Origin: config.origin }, data: { expected_revision: preference.revision, language: value } });
    expect(response.ok()).toBe(true);
    await page.evaluate(id => window.dispatchEvent(new StorageEvent('storage', { key: `account-appearance:${id}` })), config.account);
    await expect(page.locator('html')).toHaveAttribute('lang', value === 'zh' ? 'zh-Hans' : 'en');
  };
  await language('zh');
  await expect(page.getByRole('heading', { name: 'Studio 权限', exact: true })).toBeVisible();
  await expect(page.getByRole('row').filter({ hasText: '审阅集成' })).toContainText('允许');
  await page.reload();
  await expect(page.getByLabel('Studio 卷 ID', { exact: true })).toHaveValue('aster-shared-assets');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  mkdirSync(config.evidence_directory, { recursive: true });
  await page.screenshot({ path: path.join(config.evidence_directory, `${engine}-${config.width}.png`), fullPage: true });
  await language('en');
  expect(errors).toEqual([]);
  process.stdout.write(`Studio grant provisioning passed (${engine}, ${config.width}px).\n`);
} finally {
  await context.close(); await browser.close();
}
