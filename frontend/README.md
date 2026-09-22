# Project workbench

Next.js/TypeScript pages and assets for the Go Project application. The Go edge
routes product HTTP/SSE/WebSocket; the frontend owns no API forwarding routes,
database or runtime processes.

```sh
npm ci
npm run dev
npm run lint
npm run typecheck
npm test
npm run build
npm run test:ui
```

Configure the Go application's `frontend` origin to this server and open its
browser edge. Build-time branding uses `NEXT_PUBLIC_PROJECT_BRAND_NAME`.
`PROJECT_DEV_ORIGINS` declares development hosts. The interface supports English
and Chinese and stores its light/dark preference under `project-theme`.

Use the npm build/dev commands above: their hooks apply the reviewed lifecycle
fixes for the pinned presentation viewer before Next compiles it. The patch
verifies the upstream source digest and refuses an unreviewed dependency change.
It releases chart callbacks, skips removed chart targets, uses document theme
colors when explicit fills are absent, and disables preview chart animation.
See [the patch](scripts/patch-office-viewers.mjs).

See [frontend architecture](../docs/frontend.md), [configuration](../docs/configuration.md)
and [development](../docs/development.md). The Playwright suite expects the
isolated executable harness described in [dev/README.md](../dev/README.md).
