// Local qualification serves the exact standalone project packaged for delivery.
import { cpSync, rmSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { resolve } from "node:path";
export async function serveStandalone(name, port) {
  if (!["frontend", "admin-frontend"].includes(name) || !Number.isInteger(port) || port < 1 || port > 65535) throw new Error("valid frontend and port required");
  const repo = fileURLToPath(new URL("..", import.meta.url));
  const root = resolve(repo, name), runtime = resolve(root, ".next/standalone", name);
  for (const [source, target] of [[".next/static", ".next/static"], ["public", "public"]]) {
    rmSync(resolve(runtime, target), { recursive: true, force: true });
    cpSync(resolve(root, source), resolve(runtime, target), { recursive: true });
  }
  process.env.HOSTNAME = "127.0.0.1";
  process.env.PORT = String(port);
  await import(pathToFileURL(resolve(runtime, "server.js")).href);
}
if (process.argv[1] === fileURLToPath(import.meta.url)) await serveStandalone(process.argv[2], Number(process.argv[3]));
