import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";
export default defineConfig({
  resolve: { alias: { "@web": fileURLToPath(new URL("../web", import.meta.url)), "@": fileURLToPath(new URL(".", import.meta.url)) } },
  test: { exclude: ["e2e/**", "node_modules/**", ".next/**", ".next-perf/**"] },
});
