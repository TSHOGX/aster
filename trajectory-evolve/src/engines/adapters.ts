// engines/adapters.ts — every engine this pipeline might name on a leaf.
//
// callNode picks the engine per call via TE_ENGINE / --engine. dynamic-workflow
// only spawns adapters that were registered on the run, so both the offline mock
// and the real pi CLI are listed here. Swapping the agent architecture later is
// adding another adapter to this list, not rewriting the graphs.

import { pi } from "dynamic-workflow";
import { mock } from "./mock.ts";

export const engines = [mock, pi];
