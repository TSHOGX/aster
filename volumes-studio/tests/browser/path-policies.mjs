// Studio-owned policies for a manual path with binding evaluation disabled.
// Services, grants, CAS receipts and policy history are real. Lost replies are
// injected only after the server has committed the actual browser request.
import { chromium, firefox, webkit, expect } from '../../frontend/node_modules/@playwright/test/index.mjs';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
let input = '';
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
const browser = await ({ chromium, firefox, webkit }[config.browser] ?? chromium).launch({ headless: true });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: config.width, height: 900 } });
try {
  await context.addCookies(config.cookies.filter(cookie => cookie.Value && cookie.MaxAge >= 0).map(cookie => ({ name: cookie.Name, value: cookie.Value, domain: new URL(config.origin).hostname, path: '/volumes-studio/', secure: true, httpOnly: true, sameSite: 'Lax' })));
  await context.addCookies([{ name: 'project_login', value: config.central_cookie, url: config.origin, secure: true, httpOnly: true, sameSite: 'Lax' }]);
  const page = await context.newPage();
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  const root = `${config.origin}/volumes-studio/api/v1/libraries/aster-shared-assets/evaluation-policies`;
  const url = `${config.origin}/volumes-studio/libraries/aster-shared-assets?view=evaluation+policies`;
  await page.goto(url);
  const region = page.getByRole('region', { name: 'Evaluation policies', exact: true });
  await expect(region.getByLabel('Policy path', { exact: true })).toBeEnabled();
  const session = await (await page.request.get(`${config.origin}/volumes-studio/api/v1/session`)).json();
  const headers = { Origin: config.origin, 'X-CSRF-Token': session.csrf };
  const account = await (await page.request.get(`${config.origin}/api/auth/session`)).json();
  const language = async value => {
    const preference = await (await page.request.get(`${config.origin}/api/me/preferences`)).json();
    const response = await page.request.patch(`${config.origin}/api/me/preferences`, { headers: { Origin: config.origin }, data: { expected_revision: preference.revision, language: value } });
    expect(response.ok()).toBe(true);
    await page.evaluate(id => window.dispatchEvent(new StorageEvent('storage', { key: `account-appearance:${id}` })), account.id);
  };
  const grant = async (action, effect) => {
    const response = await page.request.post(`${config.origin}/fixture/path-policy-access`, { data: { action, effect } });
    expect(response.status(), await response.text()).toBe(204);
  };
  const policies = async () => {
    const response = await page.request.get(root); expect(response.ok()).toBe(true); return response.json();
  };
  const save = () => region.getByRole('button', { name: 'Save policy revision', exact: true }).click();
  await region.getByLabel('Policy path', { exact: true }).fill('skills');
  await region.getByLabel('Evaluation mode', { exact: true }).selectOption('required');
  const profileID = region.getByLabel('Evaluation profile ID', { exact: true });
  await profileID.fill('bad id');
  expect(await profileID.evaluate(element => element.checkValidity())).toBe(false);
  for (const kind of ['profile', 'service']) {
    const label = kind === 'profile' ? 'Evaluation profile' : 'Evaluation service';
    for (const [field, title] of [['id', 'ID'], ['revision', 'Revision'], ['digest', 'Digest']]) {
      await region.getByLabel(`${label} ${title}`, { exact: true }).fill(String(config.policy[kind][field]));
    }
  }
  await page.reload();
  await expect(region.getByLabel('Policy path', { exact: true })).toHaveValue('skills');
  await expect(region.getByLabel('Evaluation profile Digest', { exact: true })).toHaveValue(config.policy.profile.digest);
  await language('zh');
  const translated = page.getByRole('region', { name: '评估策略', exact: true });
  await expect(translated.getByLabel('策略路径', { exact: true })).toHaveValue('skills');
  await expect(translated.getByRole('button', { name: '保存策略修订版本', exact: true })).toBeEnabled();
  await language('en');

  let lost; const requests = [];
  await page.route(root, async route => {
    if (route.request().method() !== 'POST') return route.continue();
    requests.push(route.request().postData());
    if (!lost) {
      const response = await route.fetch(); expect(response.status()).toBe(200); lost = await response.json();
      return route.abort('failed');
    }
    return route.continue();
  });
  await save();
  await expect(region).toContainText('Policy save is unconfirmed. Recover the same request before making another change.');
  expect(lost.revision).toBe(1);
  await page.reload();
  await expect(region.getByLabel('Policy path', { exact: true })).toBeDisabled();
  await grant('volume.configure', 'deny');
  await region.getByRole('button', { name: 'Recover policy save', exact: true }).click();
  await expect(region).toContainText('Evaluation policies are unavailable. Retry or check Volume access.');
  await expect(region.getByLabel('Policy path', { exact: true })).toHaveCount(0);
  await grant('volume.read', 'deny');
  await region.getByRole('button', { name: 'Reload policies', exact: true }).click();
  await expect(region.getByLabel('Policy path', { exact: true })).toHaveCount(0);
  await grant('volume.configure', 'allow'); await grant('volume.read', 'allow');
  await region.getByRole('button', { name: 'Reload policies', exact: true }).click();
  await expect(region.getByRole('button', { name: 'Recover policy save', exact: true })).toBeEnabled();
  await region.getByRole('button', { name: 'Recover policy save', exact: true }).click();
  await expect(region).toContainText('Saved policy revision: 1');
  expect(requests).toHaveLength(3);
  expect(new Set(requests).size).toBe(1);
  expect((await policies()).items).toEqual([lost]);
  await page.unroute(root);

  await grant('volume.configure', 'deny');
  await region.getByRole('button', { name: 'Reload policies', exact: true }).click();
  await expect(region).toContainText('Volume configure access is required to save policy revisions.');
  await expect(region.getByLabel('Evaluation mode', { exact: true })).toBeDisabled();
  await expect(region.getByLabel('Existing policy', { exact: true })).toBeEnabled();
  await region.getByRole('button', { name: 'Policy revision history', exact: true }).click();
  await expect(region.getByRole('list', { name: 'Policy revision history', exact: true })).toContainText(lost.digest);
  await grant('volume.configure', 'allow');
  await region.getByRole('button', { name: 'Reload policies', exact: true }).click();
  await expect(region.getByLabel('Evaluation mode', { exact: true })).toBeEnabled();

  // A separate authorized editor wins revision 2. The browser retains its edits
  // and requires explicit review/adoption of that current CAS revision.
  await region.getByLabel('Evaluation mode', { exact: true }).selectOption('report_only');
  const concurrent = await page.request.post(root, { headers, data: { request_id: crypto.randomUUID(), expected_revision: 1, path: 'skills', evaluation: config.policy } });
  expect(concurrent.status()).toBe(200);
  await save();
  await expect(region).toContainText('Policy changed. Reload policies, review the current revision, then explicitly use it before saving your edits.');
  await region.getByRole('button', { name: 'Reload policies', exact: true }).click();
  await expect(region.getByLabel('Evaluation mode', { exact: true })).toHaveValue('report_only');
  await expect(region.getByRole('button', { name: 'Save policy revision', exact: true })).toBeDisabled();
  await region.getByText('Current policy', { exact: true }).click();
  await expect(region.locator('details').first()).toContainText('Required evaluation');
  await region.getByRole('button', { name: 'Use current policy revision', exact: true }).click();
  await save(); await expect(region).toContainText('Saved policy revision: 3');
  await region.getByLabel('Evaluation mode', { exact: true }).selectOption('required');
  await save(); await expect(region).toContainText('Saved policy revision: 4');
  await region.getByRole('button', { name: 'Policy revision history', exact: true }).click();
  const history = region.getByRole('list', { name: 'Policy revision history', exact: true });
  await expect(history.getByRole('listitem')).toHaveCount(4);
  await expect(history.getByRole('listitem').nth(1)).toContainText('Informational evaluation');
  await expect(history.getByRole('listitem').last()).toContainText(lost.digest);

  // A disabled whole-Volume policy never erases the required nested policy.
  await region.getByLabel('Existing policy', { exact: true }).selectOption('');
  await save(); await expect(region).toContainText('Saved policy revision: 1');
  expect((await policies()).items.map(item => [item.path, item.evaluation.mode])).toEqual([['', 'disabled'], ['skills', 'required']]);

  // Malformed local recovery has an explicit recovery path and preserves the
  // already committed server history.
  const storageKey = `studio-path-policy:${JSON.stringify([session.issuer, session.subject, 'aster-shared-assets'])}`;
  await page.evaluate(key => sessionStorage.setItem(key, '{invalid'), storageKey);
  await page.reload();
  await expect(region).toContainText('Local policy recovery is invalid. Discard it to continue.');
  await region.getByRole('button', { name: 'Discard policy recovery', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Discard', exact: true }).click();
  await expect(region.getByLabel('Policy path', { exact: true })).toBeEnabled();
  expect((await policies()).items).toHaveLength(2);
  await language('zh');
  await expect(translated).toContainText('路径策略适用于手动和生成的更改。');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  mkdirSync(config.evidence_directory, { recursive: true });
  await page.screenshot({ path: path.join(config.evidence_directory, `${config.browser || 'chromium'}-${config.width}-zh.png`), fullPage: true });
  await language('en');
  expect(errors).toEqual([]);
  process.stdout.write(`Path policy management passed (${config.browser || 'chromium'}, ${config.width}px).\n`);
} finally { await context.close(); await browser.close(); }
