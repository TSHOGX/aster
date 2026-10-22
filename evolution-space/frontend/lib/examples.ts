import type { Revision } from "./evolution";

export type Threshold = { name: string; unit: string; operator: "gte" | "lte"; value: number; minimum_samples: number; minimum_coverage: number };
export type ExampleField = { name: string; heading: string; required: boolean; multiline: boolean; enum?: string[]; guidance: string };
export type Baseline = { description: string; dataset: string; dataset_example: string; reference_strategy: string; thresholds: Threshold[] };
export type ExampleSummary = {
  template: string; memory_class: "episodic" | "semantic" | "procedural"; kind: string; goal: string; summary: string;
  prefix: string; layout: string; evidence: string; fields: ExampleField[]; baseline: Baseline; installed?: Revision;
};
export type ExamplePage = { items: ExampleSummary[]; example_builders: boolean };
export type ExampleWorkflow = { ref: Revision; workflow: Record<string, unknown> & { nodes?: Record<string, unknown>[]; edges?: Record<string, unknown>[] }; node_docs: Record<string, string>; installed: boolean };
export type SampleFile = { path: string; text: string };
export type SampleHeld = { id: string; title: string; hold_reason?: string; evidence_ids: string[] };
export type ExampleDetail = ExampleSummary & {
  workflows: ExampleWorkflow[]; model?: Revision;
  sample: { corpus: { episodes: { id: string; outcome: string; text: string }[] }; files: SampleFile[]; held: SampleHeld[] };
};
export type ModelChoice = Revision & { label?: string; call_tokens?: number };
export type ModelPage = { items: ModelChoice[]; example_builders: boolean; connections?: boolean };
export const pin = ({ id, revision, digest }: ModelChoice): Revision => ({ id, revision, digest });

export const memoryClasses = [
  { value: "procedural", label: "Procedural", description: "How to act" },
  { value: "semantic", label: "Semantic", description: "What is true" },
  { value: "episodic", label: "Episodic", description: "What happened" },
] as const;
export const evidenceRules: Record<string, string> = {
  all_completed: "Every cited episode must have completed.",
  resolved: "At least one cited episode must have completed. Failures can be the trigger.",
  single_case: "Exactly one source episode, with a known outcome.",
};
// Short labels for the reasons the verifier holds a proposal.
export const holdReasons: Record<string, string> = {
  unsuccessful_evidence: "Cites a failed episode", unresolved: "No completed episode shows the fix", missing_field: "A required field is empty",
  invalid_field: "A field has an invalid value", incomplete_metric: "Metric lacks formula, unit, grain or entity", outcome_mismatch: "Outcome disagrees with the evidence",
  truncated_evidence: "Evidence was truncated", unknown_provenance: "Evidence has unknown origin", sensitive_content: "Looks like a secret or personal data",
  multiple_cases: "Cites more than one case", record_failed: "The record could not be written", held_by_agent: "Held by the agent", conflict: "Conflicts with another definition",
};

// What each node kind does, shown in the palette and inspector.
export const nodeKindHelp: Record<string, string> = {
  agent: "Calls a model with a prompt. Input ports fill {{variables}} in the prompt; the model returns JSON for each output port.",
  transform: "Runs registered deterministic code, such as a file builder. Choose it by implementation revision.",
  validator: "Runs registered deterministic checks and returns the cleaned value. Use it to gate model output.",
  router: "Sends its input to one of two routes based on a pure predicate (truthy, has_items, all_valid).",
  join: "Combines outputs from different routes into one object.",
  map: "Runs a sub-workflow once per array item, with bounded concurrency.",
  subgraph: "Runs another published workflow as one step.",
  loop: "Repeats a sub-workflow until a predicate holds or the iteration limit is reached.",
};
export const nodeKindLabels: Record<string, string> = { agent: "Agent", transform: "Builder", validator: "Validator", router: "Router", join: "Join", map: "Map", subgraph: "Sub-workflow", loop: "Loop" };
