# Workbench frontend

Next.js/TypeScript UI with server-side same-origin proxy routes to the orchestrator.

```bash
npm ci
HARNESS_API_BASE_URL=http://127.0.0.1:8090 PORT=8000 npm run dev
npm run lint
npm run typecheck
npm test
npm run build
```

The production build uses standalone output. Release packaging includes static
assets and a pinned Node runtime; deployment does not run npm. The server-side
backend URL is selected at runtime through `HARNESS_API_BASE_URL` or
`ORCHESTRATOR_URL`.

Core UI areas are conversations, queue/runtime state, files, sharing, schedules
and notifications. Optional evolution routes integrate a separate external runner;
they require explicit private configuration and contain no business-schema demo
fallback. See [frontend architecture](../docs/frontend.md),
[shared assets](../docs/sandbox-image.md), and [development](../docs/development.md).

Product localization dictionaries are application content; English is the
repository documentation language, not a requirement to remove supported UI locales.
