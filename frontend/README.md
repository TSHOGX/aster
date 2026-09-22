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

See [frontend architecture](../docs/frontend.md), [configuration](../docs/configuration.md)
and [development](../docs/development.md). The Playwright suite expects the
isolated executable harness described in [dev/README.md](../dev/README.md).
