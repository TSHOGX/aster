import { execFileSync } from "node:child_process";
import { resolve } from "node:path";

export default function setup() {
  // Functional Office tests are mandatory in this suite; only profiling is opt-in.
  execFileSync(
    process.env.PYTHON || "python3",
    [resolve(__dirname, "preview-documents.py"), process.env.PREVIEW_FIXTURES!],
    { stdio: "inherit" },
  );
}
