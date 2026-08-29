// bootstrap.ts — process-level setup that MUST happen before dynamic-workflow is
// imported.
//
// `RUNS_ROOT` is resolved once at module-evaluation time inside
// dynamic-workflow's journal, so assigning the env var later in cli.ts silently
// has no effect and every journal lands in ~/.dynamic-workflow instead of this
// project's state dir. ES modules evaluate in import order, so importing this
// file first is what makes the assignment land in time.

import { resolve } from "node:path";
import { files, paths } from "./config.ts";

// The engines are peer tools resolved off PATH. `bun` itself lives in
// node_modules/.bin here, which is what lets the mock engine spawn it.
process.env.PATH = `${resolve(paths.root, "node_modules", ".bin")}:${process.env.PATH ?? ""}`;

// Keep run history inside the project: one journal per execution, one shared
// resume cache per workflow name.
process.env.DYNAMIC_WORKFLOW_RUNS_ROOT ??= files.runs;
