import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../../', import.meta.url));
/** @type {import('next').NextConfig} */
const nextConfig = {
  basePath: '/evolution-space', trailingSlash: true, poweredByHeader: false, output: 'standalone',
  turbopack: { root }, outputFileTracingRoot: root,
  async headers() {
    return [{ source: '/:path*', headers: [
      { key: 'X-Content-Type-Options', value: 'nosniff' },
      { key: 'Referrer-Policy', value: 'no-referrer' },
      { key: 'Content-Security-Policy', value: "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'" },
    ] }];
  },
};
export default nextConfig;
