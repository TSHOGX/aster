import nextCoreWebVitals from "eslint-config-next/core-web-vitals";

const config = [
  ...nextCoreWebVitals,
  {
    // The .next.bak-*/.next.backup.* entries mirror .gitignore: operational
    // backups of a build are minified bundles, and linting them buried the real
    // source findings under ~90 errors from generated code.
    ignores: [
      ".next/**",
      ".next.bak-*/**",
      ".next.backup.*/**",
      "out/**",
      "build/**",
      "next-env.d.ts"
    ]
  }
];

export default config;
