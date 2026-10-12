import { test, expect, Page } from '@playwright/test';
import { createHash } from 'node:crypto';
const origin = 'https://127.0.0.1:18443';
async function signIn(page: Page) {
  const response = await page.request.post(`${origin}/api/auth/login`, { headers: { Origin: origin }, data: { username: 'studio-admin', password: 'browser-fixture-password' } });
  expect(response.ok()).toBeTruthy();
  await page.goto('/volumes-studio/');
  await page.getByRole('link', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('button', { name: 'New library' })).toBeVisible();
}
async function createLibrary(page: Page, name: string) {
  await page.getByRole('button', { name: 'New library' }).click();
  await page.getByLabel('Name', { exact: true }).fill(name);
  await page.getByLabel('Description').fill('Reusable research source files.');
  await page.getByRole('button', { name: 'Create draft' }).click();
  await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
}
test('central login, edit, freeze, exact version and shared logout', async ({ page }) => {
  const pageErrors: string[] = []; page.on('pageerror', error => pageErrors.push(error.message));
  await signIn(page); await createLibrary(page, 'Research handbook');
  page.once('dialog', dialog => dialog.accept('readme.md'));
  await page.getByRole('button', { name: 'Text file' }).click();
  await page.getByRole('button', { name: 'Open readme.md' }).click();
  const editor = page.getByRole('textbox', { name: 'Contents of readme.md' });
  await editor.fill('# Reviewed handbook\n\nFirst version.');
  await page.getByRole('button', { name: 'Save file', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Saved to draft');
  await page.getByRole('button', { name: 'Review & publish' }).click();
  await page.getByLabel('Version label').fill('v1');
  await page.getByLabel('Release note').fill('Reviewed starter handbook.');
  await page.getByRole('button', { name: 'Publish version', exact: true }).click();
  await expect(page.locator('.version-card .badge')).toHaveText('published', { timeout: 15000 });
  await page.getByRole('button', { name: 'Inspect files' }).click();
  await page.getByRole('button', { name: 'Open readme.md' }).click();
  await expect(editor).toHaveValue('# Reviewed handbook\n\nFirst version.');
  await expect(editor).toHaveAttribute('readonly', '');
  const versionFile = await page.getByRole('link', { name: 'Download', exact: true }).getAttribute('href');
  await page.getByRole('button', { name: 'Draft root' }).click();
  await expect(page.locator('.library-heading .badge')).toContainText('Draft');
  await page.getByRole('button', { name: 'Open readme.md' }).click();
  await expect(editor).not.toHaveAttribute('readonly', '');
  await editor.fill('Later draft edit');
  await page.getByRole('button', { name: 'Save file', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Saved to draft');
  const frozen = await page.request.get(versionFile!);
  expect(await frozen.text()).toBe('# Reviewed handbook\n\nFirst version.');
  expect(frozen.headers()['content-disposition']).toContain('attachment');
  expect(frozen.headers()['content-security-policy']).toContain('sandbox');
  await page.reload(); await expect(page.getByRole('heading', { name: 'Research handbook' })).toBeVisible();
  await page.screenshot({ path: 'test-results/authoring-desktop.png', fullPage: true });
  await page.getByRole('button', { name: 'Sign out everywhere in this session' }).click();
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  expect((await page.request.get('/api/auth/session')).status()).toBe(401);
  expect(pageErrors).toEqual([]);
});

test('resume a durable upload after a browser refresh', async ({ page }) => {
  await signIn(page); await createLibrary(page, 'Resumable dataset');
  const id = page.url().split('/').filter(Boolean).at(-1)!;
  const session = await (await page.request.get('/volumes-studio/api/v1/session')).json();
  const data = Buffer.from([0, 1, 2, 3, 255, 42]);
  const hash = (bytes: Buffer) => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
  const headers = { Origin: origin, 'X-CSRF-Token': session.csrf };
  const root = `/volumes-studio/api/v1/libraries/${id}`;
  const admitted = await page.request.post(`${root}/uploads`, { headers, data: { request_id: 'resume-admit', expected_revision: 1, path: 'dataset.bin', size: '6', digest: hash(data) } });
  expect(admitted.status()).toBe(201); const upload = await admitted.json();
  const first = await page.request.put(`${root}/uploads/${upload.id}/chunks/0`, { headers: { ...headers, 'X-Chunk-Digest': hash(data.subarray(0, 3)), 'Content-Type': 'application/octet-stream' }, data: data.subarray(0, 3) });
  expect(first.ok()).toBeTruthy();
  await page.reload(); await expect(page.getByRole('region', { name: 'Upload queue' })).toContainText('dataset.bin');
  await expect(page.getByRole('button', { name: 'Review & publish' })).toBeDisabled();
  await page.getByLabel('Select files', { exact: true }).setInputFiles({ name: 'dataset.bin', mimeType: 'application/octet-stream', buffer: data });
  await expect(page.getByRole('status')).toContainText('Files saved to the draft', { timeout: 20000 });
  const completed = await (await page.request.get(`${root}/uploads/${upload.id}`)).json();
  expect(completed.state).toBe('complete'); expect(completed.offset).toBe('6');
  expect(await (await page.request.get(`${root}/draft/content?path=dataset.bin`)).body()).toEqual(data);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: 'test-results/authoring-mobile.png', fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test('same-origin session cookies and exact prefix routing', async ({ page }) => {
  await signIn(page);
  const cookies = await page.context().cookies();
  const central = cookies.find(cookie => cookie.name === 'project_login');
  const studio = cookies.find(cookie => cookie.name === '__Secure-volumes_studio_session');
  expect(central?.path).toBe('/'); expect(studio?.path).toBe('/volumes-studio/');
  expect(studio?.httpOnly).toBe(true); expect(studio?.secure).toBe(true);
  expect((await page.request.get('/distribution/v1/exports')).status()).toBe(404);
  expect((await page.request.get('/volumes-studio-other')).status()).toBe(404);
  const response = await page.request.post('/api/identity/v1/token', { data: {} });
  expect(response.status()).toBe(404);
});
