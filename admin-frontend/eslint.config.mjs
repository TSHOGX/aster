import nextCoreWebVitals from "eslint-config-next/core-web-vitals";
const config = [...nextCoreWebVitals, { ignores: [".next/**", "test-results/**", "playwright-report/**", "next-env.d.ts"] }];

export default config;
