// Exercise the same standalone layout shipped in candidate bundles.
import { cpSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const root = fileURLToPath(new URL("..", import.meta.url));
cpSync(resolve(root, ".next/static"), resolve(root, ".next/standalone/.next/static"), { recursive: true });
cpSync(resolve(root, "public"), resolve(root, ".next/standalone/public"), { recursive: true });
process.env.HOSTNAME = "127.0.0.1";
process.env.PORT = "3107";
await import("../.next/standalone/server.js");
