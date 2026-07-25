/** @type {import('next').NextConfig} */

// Security headers applied to every response. CSP is intentionally pragmatic:
// Next.js injects inline bootstrap scripts and the app ships an inline theme
// script (app/layout.tsx), so a nonce-only policy would require middleware
// wiring; 'unsafe-inline' for script/style is the tradeoff. The real agent
// output is rendered via react-markdown (no rehype-raw) and highlight.js
// (HTML-escaped), so stored-HTML XSS is already contained. connect-src stays
// 'self' because the browser only talks to the same-origin /api proxy.
// `next dev` compiles React Refresh's runtime by evaluating a string, so the dev
// server cannot run under a CSP without 'unsafe-eval' (the page dies with
// "Uncaught EvalError" and renders blank). Production builds do not need it, so
// it is added only in development and never weakens the deployed policy.
const isDev = process.env.NODE_ENV === "development";
const scriptSrc = ["'self'", "'unsafe-inline'", ...(isDev ? ["'unsafe-eval'"] : [])].join(" ");

// Dev over plain http also needs the HMR websocket, which connects to the host
// the browser used rather than to 'self'.
const connectSrc = ["'self'", "blob:", ...(isDev ? ["ws:", "wss:"] : [])].join(" ");

const csp = [
  "default-src 'self'",
  `script-src ${scriptSrc}`,
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob:",
  "font-src 'self' data:",
  // blob: is required by the Office/xlsx preview viewer (@js-preview/excel),
  // which reads the workbook by XHR'ing a blob: object URL it creates from the
  // fetched ArrayBuffer. blob: URLs are same-origin, opaque, and can't exfiltrate
  // to a remote host, so this does not weaken the exfiltration guarantee.
  `connect-src ${connectSrc}`,
  "frame-ancestors 'none'",
  "base-uri 'self'",
  "form-action 'self'",
  "object-src 'none'"
].join("; ");

const securityHeaders = [
  { key: "Content-Security-Policy", value: csp },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=(), payment=()" },
  { key: "Cross-Origin-Opener-Policy", value: "same-origin" }
];

const nextConfig = {
  reactStrictMode: true,
  // Dev-only: hosts the browser may load the dev server from. Deployments differ
  // (ECS public IP vs. a local VM's address), so extra hosts come from
  // HARNESS_DEV_ORIGINS (comma-separated) rather than being hardcoded per env.
  allowedDevOrigins: [
    "123.249.65.231",
    ...(process.env.HARNESS_DEV_ORIGINS ?? "")
      .split(",")
      .map((origin) => origin.trim())
      .filter(Boolean)
  ],
  async headers() {
    return [
      {
        // Everything EXCEPT /artifacts/*. Artifact responses carry their own
        // framing + CSP headers straight from the orchestrator (see
        // getArtifact): HTML is framable SAMEORIGIN so the workbench preview
        // iframe can load it. If we also emitted X-Frame-Options here the two
        // values would collide and the browser would fall back to blocking the
        // frame, which is exactly the "refused to connect" preview bug.
        source: "/((?!artifacts/).*)",
        headers: securityHeaders
      }
    ];
  }
};

export default nextConfig;
