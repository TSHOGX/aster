import { test, expect } from '@playwright/test';
import { adminFixture } from './fixture';

for (const profile of ['authored-v1', 'shared-assets-v1']) test(`select exact ${profile} release, recover a lost admission reply and retain offline local availability`, async ({ page }) => {
  await adminFixture(page);
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
    if (url.pathname === '/api/admin/volume-imports') return route.fulfill({ json: { imports: admitted ? [imported] : [] } });
    return route.fallback();
  });
  await page.route('**/api/admin/runtime/options?*', route => route.fulfill({ json: { max_mounts: '8', options: [] } }));
  await page.goto('/admin/resources/volumes?volume_publisher=publisher&volume_library=library&volume_version=version');
  await expect(page.getByRole('link', { name: 'Open Volume Studio' })).toHaveAttribute('href', '/volumes-studio/');
  await expect(page.getByText('Reviewed handbook')).toBeVisible();
  await page.getByRole('button', { name: 'Import release', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Check original request' })).toBeVisible();
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
