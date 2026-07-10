/** @type {import('next').NextConfig} */

// Security headers applied to every response. CSP is intentionally pragmatic:
// Next.js injects inline bootstrap scripts and the app ships an inline theme
// script (app/layout.tsx), so a nonce-only policy would require middleware
// wiring; 'unsafe-inline' for script/style is the tradeoff. The real agent
// output is rendered via react-markdown (no rehype-raw) and highlight.js
// (HTML-escaped), so stored-HTML XSS is already contained. connect-src stays
// 'self' because the browser only talks to the same-origin /api proxy.
const csp = [
  "default-src 'self'",
  "script-src 'self' 'unsafe-inline'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob:",
  "font-src 'self' data:",
  "connect-src 'self'",
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
  allowedDevOrigins: ["123.249.65.231"],
  async headers() {
    return [
      {
        source: "/:path*",
        headers: securityHeaders
      }
    ];
  }
};

export default nextConfig;
