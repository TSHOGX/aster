import path from "node:path";
import { fileURLToPath } from "node:url";
const directory = path.dirname(fileURLToPath(import.meta.url));
const development = process.env.NODE_ENV === "development";
const nextConfig = {
  basePath: "/admin",
  output: "standalone",
  outputFileTracingRoot: path.resolve(directory, ".."),
  turbopack: { root: path.resolve(directory, "..") },
  reactStrictMode: true,
  compiler: { removeConsole: { exclude: ["warn", "error"] } },
  async headers() {
    return [{ source: "/:path*", headers: [
      { key: "Content-Security-Policy", value: `default-src 'self'; script-src 'self' 'unsafe-inline'${development ? " 'unsafe-eval'" : ""}; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'${development ? " ws: wss:" : ""}; base-uri 'self'; form-action 'self'; frame-ancestors 'self'; object-src 'none'` },
      { key: "X-Content-Type-Options", value: "nosniff" },
      { key: "Referrer-Policy", value: "no-referrer" },
      { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=(), payment=()" },
      { key: "Cross-Origin-Opener-Policy", value: "same-origin" },
    ] }];
  },
};

export default nextConfig;
