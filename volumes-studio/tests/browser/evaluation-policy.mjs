// Real-process evaluator policy acceptance. All report/merge requests reach the
// actual Studio and Evolution Space services; only the evaluator is deterministic.
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
  const integrationPath = `/volumes-studio/libraries/aster-shared-assets?branch=${config.branch}&integration=${encodeURIComponent(config.prepared)}`;
  const root = `${config.origin}/volumes-studio/api/v1/libraries/aster-shared-assets`;
  const api = async () => {
    const response = await page.request.get(`${root}/integrations/${encodeURIComponent(config.prepared)}`);
    expect(response.ok()).toBe(true); return response.json();
  };
  const canMerge = config.mode !== 'required';
  const pending = config.outcome === 'pending';
  await page.goto(config.origin + integrationPath);
  const checks = page.getByRole('region', { name: 'Evaluation checks', exact: true });
  await expect(checks).toBeVisible();
  const approve = page.getByRole('button', { name: 'Approve integration', exact: true });
  const merge = async () => {
    await expect(approve).toBeEnabled(); await approve.click();
    const accept = page.getByRole('button', { name: 'Merge approved integration', exact: true });
    await expect(accept).toBeEnabled(); await accept.click(); await page.getByRole('dialog', { name: 'Merge into main' }).getByRole('button', { name: 'Merge', exact: true }).click();
    await expect(page.getByText('Acceptance is complete. Publishing creates a version; it does not deploy to Projects.')).toBeVisible();
  };
  const account = await (await page.request.get(`${config.origin}/api/auth/session`)).json();
  const language = async value => {
    const preference = await (await page.request.get(`${config.origin}/api/me/preferences`)).json();
    const response = await page.request.patch(`${config.origin}/api/me/preferences`, { headers: { Origin: config.origin }, data: { expected_revision: preference.revision, language: value } });
    expect(response.ok()).toBe(true);
    await page.evaluate(id => window.dispatchEvent(new StorageEvent('storage', { key: `account-appearance:${id}` })), account.id);
  };
  if (config.mode === 'disabled') {
    await expect(checks).toContainText('Evaluation is off. No benchmark result was requested.');
    await expect(checks.getByRole('button', { name: 'Request evaluation', exact: true })).toHaveCount(0);
    expect((await api()).evaluation).toBe('not_requested');
    await language('zh');
    await expect(page.getByRole('region', { name: '评估检查', exact: true })).toContainText('评估已关闭，未请求基准测试结果。');
    await language('en');
    await merge();
  } else {
    if (canMerge) {
      await expect(approve).toBeEnabled();
      await expect(checks).toContainText('Informational checks do not block approval or merge.');
      await expect(checks).toContainText('Informational evaluation');
    } else await expect(approve).toBeDisabled();
    await checks.getByLabel('Evaluation token limit', { exact: true }).fill('1000');
    await checks.getByLabel('Evaluation cost limit (micros)', { exact: true }).fill('10000');
    await checks.getByLabel('Evaluation timeout (seconds)', { exact: true }).fill('60');
    await language('zh');
    const translated = page.getByRole('region', { name: '评估检查', exact: true });
    if (canMerge) await expect(translated).toContainText('参考评估不会阻止审批或合并。');
    else await expect(translated).toContainText('必需的检查需要此精确集成结果的通过证据。');
    await language('en');
    await checks.getByRole('button', { name: 'Request evaluation', exact: true }).click();
    if (pending) {
      await expect.poll(async () => (await (await page.request.get(`${config.origin}/fixture/evaluation-dispatched`)).json()).count).toBe(1);
      expect((await api()).evaluations[0].outcome).toBe('pending');
      if (canMerge) await merge();
      else { await expect(approve).toBeDisabled(); await expect(page.getByRole('button', { name: 'Merge approved integration', exact: true })).toBeDisabled(); }
      const released = await page.request.post(`${config.origin}/fixture/evaluation-release`); expect(released.status()).toBe(204);
    }
    const expected = config.outcome === 'pending' ? 'fail' : config.outcome === 'error' ? 'inconclusive' : config.outcome;
    await expect(async () => {
      const refresh = checks.getByRole('button', { name: 'Refresh evaluation', exact: true });
      if (await refresh.isVisible()) await refresh.click();
      const view = await api();
      expect(view.evaluations[0].outcome).toBe(expected);
      expect(view.evaluations[0].state).toBe(config.outcome === 'error' ? 'error' : 'completed');
    }).toPass({ timeout: 20000, intervals: [200, 500, 1000] });
    await page.reload(); await expect(checks).toContainText(expected);
    if (canMerge && !pending) await merge();
    if (!canMerge) {
      await expect(approve).toBeDisabled();
      await expect(page.getByRole('button', { name: 'Merge approved integration', exact: true })).toBeDisabled();
      const session = await (await page.request.get(`${config.origin}/volumes-studio/api/v1/session`)).json();
      for (const [path, body] of [['reviews', { verdict: 'approve' }], ['merge', { expected_main: (await api()).identity.main.commit_id }]]) {
        const response = await page.request.post(`${root}/integrations/${path}`, { headers: { Origin: config.origin, 'X-CSRF-Token': session.csrf }, data: { ...body, request_id: crypto.randomUUID(), prepared_digest: config.prepared } });
        expect(response.status()).toBe(409);
      }
    }
  }
  const final = await api();
  expect(final.acceptance).toBe(canMerge ? 'accepted' : 'not_accepted');
  const main = await (await page.request.get(`${root}/main`)).json();
  expect(main.commit_id).toBe(canMerge ? final.identity.result.commit_id : final.identity.main.commit_id);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  expect(errors).toEqual([]);
  mkdirSync(config.evidence_directory, { recursive: true });
  await page.screenshot({ path: path.join(config.evidence_directory, `${config.mode}-${config.outcome}.png`), fullPage: true });
  process.stdout.write(`Evaluation policy journey passed (${config.mode}/${config.outcome}, ${config.browser}, ${config.width}px).\n`);
} finally { await context.close(); await browser.close(); }
