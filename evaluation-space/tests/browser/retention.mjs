// Actual native run, offline operator expiry and restarted browser API.
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
  const login = page.getByRole('link', { name: 'Sign in', exact: true });
  await expect(login).toBeVisible(); await login.click();
  await expect(page.getByRole('heading', { name: `Run inspector: ${config.run}`, exact: true })).toBeVisible();
  const session = await (await page.request.get(`${config.origin}/api/auth/session`)).json();
  const language = async value => {
    const current = await (await page.request.get(`${config.origin}/api/me/preferences`)).json();
    expect((await page.request.patch(`${config.origin}/api/me/preferences`, { headers: { Origin: config.origin }, data: { expected_revision: current.revision, language: value } })).ok()).toBe(true);
    await page.evaluate(id => window.dispatchEvent(new StorageEvent('storage', { key: `account-appearance:${id}` })), session.id);
    await expect(page.locator('html')).toHaveAttribute('lang', value === 'zh' ? 'zh-Hans' : 'en');
  };
  const artifact = await page.request.get(`${root}/artifacts/${config.artifact}`);
  if (config.phase === 'available') {
    expect(artifact.status()).toBe(200);
    await expect(page.getByRole('heading', { name: 'Node attempts', exact: true })).toBeVisible();
    await language('zh');
    await expect(page.getByRole('heading', { name: '节点尝试', exact: true })).toBeVisible();
  } else {
    expect(artifact.status()).toBe(config.phase === 'expired' ? 410 : 403);
    expect(artifact.headers()['cache-control']).toContain('no-store');
    expect(artifact.headers()['x-content-type-options']).toBe('nosniff');
    const expected = config.phase === 'expired' ? 'Run evidence has expired. Retained run metadata is still available.' : 'Run evidence is unavailable. Retry or check source access.';
    await expect(page.getByRole('main').getByRole('alert')).toContainText(expected);
    await expect(page.locator('section').filter({ has: page.getByRole('heading', { name: 'Node attempts', exact: true }) }).locator('ol > li')).toHaveCount(0);
    expect((await page.request.get(`${root}/attempts`)).status()).toBe(config.phase === 'expired' ? 410 : 403);
    if (config.phase === 'expired') {
      const metadata = await (await page.request.get(`${root}/artifacts`)).json();
      expect(metadata.items.some(item => item.id === config.artifact)).toBe(true);
    }
    await language('zh');
    await expect(page.getByRole('main').getByRole('alert')).toContainText(config.phase === 'expired' ? '运行证据已过期，保留的运行元数据仍可查看。' : '运行证据不可用，请重试或检查源访问权限。');
    await page.reload();
    await expect(page.getByRole('main').getByRole('alert')).toContainText(config.phase === 'expired' ? '运行证据已过期' : '运行证据不可用');
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  mkdirSync(config.evidence_directory, { recursive: true });
  await page.screenshot({ path: path.join(config.evidence_directory, `${config.phase}-${config.width}.png`), fullPage: true });
  await language('en');
  expect(errors).toEqual([]);
  process.stdout.write(`Native artifact retention ${config.phase} passed (${config.browser}, ${config.width}px).\n`);
} finally { await context.close(); await browser.close(); }
