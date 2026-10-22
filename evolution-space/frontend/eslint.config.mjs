import nextCoreWebVitals from 'eslint-config-next/core-web-vitals';
const config = [...nextCoreWebVitals, { ignores: ['.next/**', 'next-env.d.ts', 'test-results/**', 'playwright-report/**'] }];
export default config;
