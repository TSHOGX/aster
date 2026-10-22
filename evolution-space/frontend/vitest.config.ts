import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";
export default defineConfig({
  resolve: { alias: { "@": fileURLToPath(new URL(".", import.meta.url)), "@web": fileURLToPath(new URL("../../web", import.meta.url)) } },
  test: { exclude: ["e2e/**", "node_modules/**", ".next/**"] },
});
