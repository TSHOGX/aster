// Invoked by TestRequiredEvaluationRealProcesses with disposable service/session
// coordinates on stdin. Requests use a local TLS front door and real Studio gateway.
import { chromium, firefox, webkit, expect } from '../../frontend/node_modules/@playwright/test/index.mjs';
let input = '';
for await (const chunk of process.stdin) input += chunk;
const config = JSON.parse(input);
const browser = await ({ chromium, firefox, webkit }[config.browser] ?? chromium).launch({ headless: true });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: config.width, height: 900 } });
try {
  await context.addCookies(config.cookies.map(cookie => ({
    name: cookie.Name, value: cookie.Value, domain: new URL(config.origin).hostname, path: '/volumes-studio/',
    secure: true, httpOnly: true, sameSite: 'Lax',
  })).filter(cookie => cookie.value));
  await context.addCookies([{name:'project_login',value:config.central_cookie,domain:new URL(config.origin).hostname,path:'/',secure:true,httpOnly:true,sameSite:'Lax'}]);
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(`${config.origin}/volumes-studio/libraries/aster-shared-assets?branch=${config.branch}&integration=${encodeURIComponent(config.prepared)}`);
  const checks = page.getByRole('region', { name: 'Evaluation checks', exact: true });
  await expect(checks).toBeVisible();
  await expect(page.getByRole('button', { name: 'Approve integration', exact: true })).toBeDisabled();
  await checks.getByLabel('Evaluation token limit', { exact: true }).fill('1000');
  await checks.getByLabel('Evaluation cost limit (micros)', { exact: true }).fill('10000');
  await checks.getByLabel('Evaluation timeout (seconds)', { exact: true }).fill('60');
  const account = await (await page.request.get(`${config.origin}/api/auth/session`)).json();
  const language = async value => {
    const preference = await (await page.request.get(`${config.origin}/api/me/preferences`)).json();
    const response = await page.request.patch(`${config.origin}/api/me/preferences`, {headers:{Origin:config.origin},data:{expected_revision:preference.revision,language:value}});
    expect(response.ok()).toBe(true);
    await page.evaluate(id=>window.dispatchEvent(new StorageEvent('storage',{key:`account-appearance:${id}`})),account.id);
  };
  await language('zh');
  const translated=page.getByRole('region',{name:'评估检查',exact:true});
  await expect(translated.getByLabel('评估令牌上限',{exact:true})).toHaveValue('1000');
  await expect(translated.getByRole('button',{name:'请求评估',exact:true})).toBeVisible();
  await language('en');
  await checks.getByRole('button', { name: 'Request evaluation', exact: true }).click();
  await expect(checks.getByRole('button', { name: 'Refresh evaluation', exact: true })).toBeVisible();
  if (config.cancel_first) {
    await expect.poll(async()=> (await (await page.request.get(`${config.origin}/fixture/evaluation-dispatched`)).json()).count).toBe(1);
    await checks.getByRole('button',{name:'Cancel evaluation',exact:true}).click();
    await page.getByRole('dialog',{name:'Cancel evaluation',exact:true}).getByRole('button',{name:'Cancel evaluation',exact:true}).click();
    await expect(checks).toContainText('Cancellation requested; spending remains reserved until reconciliation finishes.');
    await expect(async()=>{
      const refresh=checks.getByRole('button',{name:'Refresh evaluation',exact:true});
      if(await refresh.isVisible())await refresh.click();
      await expect(checks.getByRole('button',{name:'Request evaluation',exact:true})).toBeEnabled();
      await expect(checks).toContainText('inconclusive');
    }).toPass({timeout:20000,intervals:[200,500,1000]});
    await expect(page.getByRole('button',{name:'Approve integration',exact:true})).toBeDisabled();
    await page.reload();
    await expect(checks).toContainText('inconclusive');
    await checks.getByLabel('Evaluation token limit',{exact:true}).fill('1000');
    await checks.getByLabel('Evaluation cost limit (micros)',{exact:true}).fill('10000');
    await checks.getByLabel('Evaluation timeout (seconds)',{exact:true}).fill('60');
    await checks.getByRole('button',{name:'Request evaluation',exact:true}).click();
  }

  await expect(async () => {
    const refresh = checks.getByRole('button', { name: 'Refresh evaluation', exact: true });
    if (await refresh.isVisible()) await refresh.click();
    await expect(checks).toContainText('Evaluation evidence passed for this exact integrated result.');
  }).toPass({ timeout: 20000, intervals: [200, 500, 1000] });
  await page.reload();
  await expect(checks).toContainText('Evaluation evidence passed for this exact integrated result.');
  await page.getByRole('button', { name: 'Approve integration', exact: true }).click();
  await page.getByRole('button', { name: 'Merge approved integration', exact: true }).click();
  await expect(page.getByText('Acceptance is complete. Publishing creates a version; it does not deploy to Projects.')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  expect(errors).toEqual([]);
  process.stdout.write(`Required evaluation browser journey passed (${config.browser}, ${config.width}px).\n`);
} finally {
  await context.close();
  await browser.close();
}
