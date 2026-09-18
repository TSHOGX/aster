import { cpSync, mkdirSync } from "node:fs";
import { spawn } from "node:child_process";
mkdirSync(".next-perf/standalone/.next-perf", { recursive: true });
cpSync(".next-perf/static", ".next-perf/standalone/.next-perf/static", {
  recursive: true,
});
cpSync("public", ".next-perf/standalone/public", { recursive: true });
const child = spawn(process.execPath, [".next-perf/standalone/server.js"], {
  stdio: "inherit",
  env: {
    ...process.env,
    HARNESS_PERF_FIXTURES: "1",
    PORT: "3187",
    HOSTNAME: "127.0.0.1",
  },
});
for (const signal of ["SIGTERM", "SIGINT"])
  process.on(signal, () => child.kill(signal));
child.on("exit", (code) => process.exit(code ?? 0));
