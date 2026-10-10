import path from "node:path";
import { fileURLToPath } from "node:url";

/** @type {import('next').NextConfig} */

// Security headers applied to every response. CSP is intentionally pragmatic:
// Next.js injects inline bootstrap scripts and the app ships an inline theme
// script (app/layout.tsx), so a nonce-only policy would require middleware
// wiring; 'unsafe-inline' for script/style is the tradeoff. The real agent
// output is rendered via react-markdown (no rehype-raw) and highlight.js
// (HTML-escaped), so stored-HTML XSS is already contained. connect-src stays
// 'self' because the browser only talks to the same-origin Go edge.
// `next dev` compiles React Refresh's runtime by evaluating a string, so the dev
// server cannot run under a CSP without 'unsafe-eval' (the page dies with
// "Uncaught EvalError" and renders blank). Production builds do not need it, so
// it is added only in development and never weakens the deployed policy.
const isDev = process.env.NODE_ENV === "development";
const scriptSrc = [
  "'self'",
  "'unsafe-inline'",
  ...(isDev ? ["'unsafe-eval'"] : []),
].join(" ");

// Dev over plain http also needs the HMR websocket, which connects to the host
// the browser used rather than to 'self'.
const connectSrc = ["'self'", ...(isDev ? ["ws:", "wss:"] : [])].join(" ");

// Portal embedding is supported. Operators should restrict frame ancestors at
// their outer edge when embedding is unnecessary. Cookie and origin checks live
// in the Go application; they do not prevent clickjacking inside an allowed frame.
const frameAncestorsDirective = "frame-ancestors *";

const csp = [
  "default-src 'self'",
  `script-src ${scriptSrc}`,
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob:",
  "font-src 'self' data:",
  `connect-src ${connectSrc}`,
  "frame-src 'self' blob:",
  frameAncestorsDirective,
  "base-uri 'self'",
  "form-action 'self'",
  "object-src 'none'",
].join("; ");

const securityHeaders = [
  { key: "Content-Security-Policy", value: csp },
  // No X-Frame-Options: see frameAncestorsDirective above.
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  {
    key: "Permissions-Policy",
    value: "camera=(), microphone=(), geolocation=(), payment=()",
  },
  { key: "Cross-Origin-Opener-Policy", value: "same-origin" },
];

const nextConfig = {
  distDir: ".next",
  output: "standalone",
  outputFileTracingRoot: path.dirname(fileURLToPath(import.meta.url)),
  reactStrictMode: true,
  // Document contents do not become console retention roots.
  compiler: { removeConsole: { exclude: ["warn", "error"] } },
  // Dev-only: hosts the browser may load the dev server from. Every deployment
  // is reached at a different address (a cloud VM's public IP, a local VM's LAN
  // address), so the list comes entirely from PROJECT_DEV_ORIGINS
  // (comma-separated) — no address is committed here. Unset means localhost only,
  // which is all `next dev` allows by default.
  allowedDevOrigins: (process.env.PROJECT_DEV_ORIGINS ?? "")
    .split(",")
    .map((origin) => origin.trim())
    .filter(Boolean),
  async headers() {
    return [
      {
        source: "/_next/static/:path*",
        headers: [{ key: "Access-Control-Allow-Origin", value: "*" }],
      },
      {
        // Next serves pages and assets. The Go edge routes /api directly to Go,
        // where file responses receive their own confined preview policy.
        source: "/:path*",
        headers: securityHeaders,
      },
      {
        source: "/office/:path*",
        headers: [
          {
            key: "Content-Security-Policy",
            value:
              "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src data: blob:; font-src 'self' data:; connect-src blob:; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'",
          },
          { key: "Referrer-Policy", value: "no-referrer" },
          { key: "Access-Control-Allow-Origin", value: "*" },
          { key: "Cache-Control", value: "public, max-age=31536000, immutable" },
        ],
      },
      {
        // The public share page carries its bearer token in the URL PATH, so the
        // app-wide strict-origin-when-cross-origin is not enough: it would send
        // the origin, but any outbound navigation from a shared page must not
        // send the path. This rule is listed after the catch-all so it wins for
        // /s/*; everything else in securityHeaders still applies.
        source: "/s/:token*",
        headers: [
          ...securityHeaders,
          { key: "Referrer-Policy", value: "no-referrer" },
        ],
      },
    ];
  },
};

export default nextConfig;
