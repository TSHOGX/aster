import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { mkdirSync, rmSync } from "node:fs";

export default function setup() {
  // Functional Office tests are mandatory in this suite; only profiling is opt-in.
  execFileSync(process.env.PYTHON || "python3", [
    resolve(__dirname, "preview-documents.py"),
    process.env.PREVIEW_FIXTURES!,
  ], { stdio: "inherit" });
  const worker=resolve(process.env.PREVIEW_FIXTURES!,"preview-worker");
  execFileSync("go",["build","-o",worker,"./cmd/preview-worker"],{cwd:resolve(__dirname,"../../orchestrator"),stdio:"inherit"});
  for (const name of ["small.docx","large.docx","small.xlsx","large.xlsx","charts.xlsx","charts-updated.xlsx","corpus.docx","corpus.xlsx"]) {
    const directory=resolve(process.env.PREVIEW_FIXTURES!,`${name}.model`);rmSync(directory,{recursive:true,force:true});mkdirSync(directory,{recursive:true});
    execFileSync(worker,[resolve(process.env.PREVIEW_FIXTURES!,name),directory,name.split(".").pop()!],{stdio:"inherit"});
  }
}
