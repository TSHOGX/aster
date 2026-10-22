import type { Binding, Budget, Configuration, Revision, Source } from "./evolution";
export type EvaluationPolicy = { mode: "disabled" | "report_only" | "required"; reference_strategy: "none" | "generation_base" | "target_main" | "pinned_release"; profile?: Revision; service?: Revision; reference_release?: { publisher_id: string; library_id: string; version_id: string; descriptor_digest: string } };
export type InputBinding = { sources?: string[]; dependency?: { binding_id: string; output_port: string }; artifact?: { id: string; group_id: string; schema_digest: string; digest: string; size: number; media_type: string; grant_id: string; policy_revision: number } };
export type ConfigBinding = Omit<Binding, "output" | "inputs"> & { output: Binding["output"] & { reservation_generation: number }; inputs: Record<string, InputBinding>; parameters: Record<string, unknown>; evaluation: EvaluationPolicy };
export type EditableConfiguration = Omit<Configuration, "bindings"> & { bindings: ConfigBinding[] };
export type TemplateDefinition = { ref: Revision; goal: string; profile: string; workflows: Revision[]; suggested_prefix: string; inputs: Record<string, string>; outputs: Record<string, string>; parameters_schema: Record<string, unknown>; evaluation: EvaluationPolicy };
export type ServiceDefinition = { ref: Revision; kind: string; enabled: boolean; capabilities: { node_kinds: string[]; workflow_versions: number[] } };
export type WorkflowDefinition = { id: string; schema_version: number; inputs: Record<string, string>; outputs: Record<string, string>; parameters_schema: Record<string, unknown>; nodes: { kind: string }[] };
export type CatalogEntry<T = unknown> = { ref: Revision; definition: T; status?: { id: string; revision: number; enabled: boolean } };
export type CatalogPage = { revisions: Revision[]; next_id: string; next_revision: number; has_more: boolean };
export type BindingInheritance = { service: boolean; evaluation: boolean };
export type DraftSettings = { service?: Revision; evaluation?: EvaluationPolicy; bindings: Record<string, BindingInheritance> };
export const initialSettings = (): DraftSettings => ({ evaluation: { ...offPolicy }, bindings: {} });
export type Draft = { revision: number; configuration: EditableConfiguration; settings?: DraftSettings };
export type Preview = { catalog_valid?: boolean; catalog_error?: string; draft_revision: number; configuration_revision: number; active_revision: number; admitted_runs: number; central_valid: boolean; digest?: string; issues: string[]; validation_error?: string; required_participants: string[]; configuration: EditableConfiguration; changes: { binding_id: string; kind: string; before?: ConfigBinding; after?: ConfigBinding }[] };
export type Operation = { operation_id: string; revision: number; state: string; current: boolean; participants: { participant: string; state: string; error?: string }[] };
export const emptyRef: Revision = { id: "", revision: 0, digest: "" };
export const offPolicy: EvaluationPolicy = { mode: "disabled", reference_strategy: "none" };
export const initialBudget: Budget = { max_tokens: 500000, max_cost_micros: 1000000, max_attempts: 20, timeout_seconds: 3600, max_artifact_bytes: 4194304 };
export const refKey = (ref: Revision) => `${ref.id}@${ref.revision}:${ref.digest}`;
export function freshConfiguration(group: string): EditableConfiguration { return { group_id: group, revision: 0, auto_enabled: false, sources: [], bindings: [], budget: { ...initialBudget } }; }
export function draftConfiguration(config: EditableConfiguration): EditableConfiguration { return { ...config, revision: 0, auto_enabled: false }; }
export function bindingFromTemplate(template: TemplateDefinition, service: Revision, budget: Budget, existing: ConfigBinding[]): ConfigBinding {
  let id = template.ref.id, n = 2; while (existing.some(binding => binding.id === id)) id = `${template.ref.id.slice(0, 112)}-${n++}`;
  const properties = template.parameters_schema.properties as Record<string, { default?: unknown }> | undefined;
  const parameters = Object.fromEntries(Object.entries(properties || {}).filter(([, value]) => value.default !== undefined).map(([key, value]) => [key, structuredClone(value.default)]));
  return { id, enabled: true, template: template.ref, workflow: template.workflows[0], service: { ...service }, source_ids: [], inputs: {}, parameters, output: { library_id: "", prefix: template.suggested_prefix, profile: template.profile, reservation_generation: 1 }, budget: { ...budget }, evaluation: structuredClone(template.evaluation), trigger: "manual", depends_on: [] };
}
export function selectSource(config: EditableConfiguration, source: Source, selected: boolean): EditableConfiguration {
  if (selected) return { ...config, sources: [...config.sources.filter(value => value.id !== source.id), source] };
  return { ...config, sources: config.sources.filter(value => value.id !== source.id), bindings: config.bindings.map(binding => ({ ...binding, source_ids: binding.source_ids.filter(id => id !== source.id), inputs: Object.fromEntries(Object.entries(binding.inputs || {}).map(([port, input]) => [port, input.sources ? { sources: input.sources.filter(id => id !== source.id) } : input])) })) };
}
// Contract integers must round-trip exactly. Arbitrary workflow parameters are
// independently validated by their published JSON schema.
export function configurationIntegersAreSafe(config: EditableConfiguration, settings?: DraftSettings): boolean {
  const safePolicy = (policy?: EvaluationPolicy) => [policy?.profile?.revision, policy?.service?.revision].every(value => value === undefined || Number.isSafeInteger(value));
  const safeBudget = (budget: Budget) => Object.values(budget).every(Number.isSafeInteger);
  return (settings?.service === undefined || Number.isSafeInteger(settings.service.revision)) && safePolicy(settings?.evaluation) && Number.isSafeInteger(config.revision) && safeBudget(config.budget) && config.sources.every(source => Number.isSafeInteger(source.policy_revision)) && config.bindings.every(binding => safePolicy(binding.evaluation) && safeBudget(binding.budget) && [binding.template.revision, binding.workflow.revision, binding.service.revision, binding.output.reservation_generation].every(Number.isSafeInteger) && Object.values(binding.inputs || {}).every(input => !input.artifact || [input.artifact.size, input.artifact.policy_revision].every(Number.isSafeInteger)));
}
