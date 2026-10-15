export type Revision = { id: string; revision: number; digest: string };
export const budgetFields = ["max_tokens", "max_cost_micros", "max_attempts", "timeout_seconds", "max_artifact_bytes"] as const;
export type BudgetField = typeof budgetFields[number];
export type Budget = Record<BudgetField, number>;
export type BudgetText = Record<BudgetField, string>;
export type Source = { id: string; kind: "project" | "session" | "volume"; resource_id: string; policy_revision: number; exclusions: string[] | null; derivative_publication: boolean; audience_groups: string[] };
export type Binding = { id: string; enabled: boolean; template: Revision; workflow: Revision; service: Revision; source_ids: string[]; output: { library_id: string; prefix: string; profile: string }; budget: Budget; trigger: string; schedule?: string; timezone?: string; depends_on: string[] | null; inputs?: Record<string, { sources?: string[]; dependency?: { binding_id: string; output_port: string }; artifact?: unknown }> };
export type Configuration = { group_id: string; revision: number; auto_enabled: boolean; sources: Source[]; bindings: Binding[]; budget: Budget };
export type ConfigurationState = { group_id: string; desired_revision: number; active_revision: number; auto_enabled: boolean; state: string; operation_id: string };
export type Permissions = { sources: { source_id: string; valid: boolean }[]; account_id: string; can_manage: boolean; can_run: boolean; can_read_evidence: boolean; can_backfill: boolean; can_manage_grants: boolean; evidence_grant_revision: number };
export type Collector = { source_id: string; source_revision: number; configuration_revision: number; baseline_event: number; after_event: number; sealed_through: number; snapshot_id: string };
export type Trigger = { id: string; binding_id: string; configuration_revision: number; kind: string; state: string; first_at: string; latest_at: string; run_id: string; pending_reason: string; dispatch_state: string; dispatch_error: string; ranges: { source_id: string; source_revision: number; after_sequence: number; through_sequence: number }[] };
export type TriggerPage = { items: Trigger[]; next: number };
export type Backfill = { download_inputs: Record<string, string[]> | null; id: string; requested_by: string; digest: string; configuration_revision: number; from: string; until: string; maximum_tokens: number; maximum_cost_micros: number; budgets: Record<string, Budget> | null; runs: Record<string, string> | null; state: string; last_error: string; episodes: { source_id: string; execution_id: string; started_at: string; digest: string; capture_state: string; family_id: string; family_kind: string; outcome: string; truncated: boolean }[] | null };
export const budgetLabels: Record<BudgetField, string> = { max_tokens: "Maximum tokens", max_cost_micros: "Maximum cost (micros)", max_attempts: "Maximum attempts", timeout_seconds: "Timeout (seconds)", max_artifact_bytes: "Artifact size limit (bytes)" };
export function budgetText(budget: Budget): BudgetText {
  return Object.fromEntries(budgetFields.map(key => [key, String(Math.min(budget[key], Number.MAX_SAFE_INTEGER))])) as BudgetText;
}
export function dependencySelection(bindings: Binding[], selected: string[]): string[] {
  const result = new Set<string>();
  function visit(id: string) {
    if (result.has(id)) return;
    const binding = bindings.find(value => value.id === id);
    if (!binding?.enabled) throw new Error("A selected binding or prerequisite is unavailable.");
    result.add(id);
    for (const parent of binding.depends_on || []) visit(parent);
  }
  for (const id of selected) visit(id);
  return [...result].sort();
}
export function parseBackfillBudgets(bindings: Binding[], selected: string[], values: Record<string, BudgetText>): Record<string, Budget> {
  const ids = dependencySelection(bindings, selected);
  if (!ids.length) throw new Error("Choose at least one binding.");
  if (!ids.some(id => Object.values(bindings.find(binding => binding.id === id)!.inputs || {}).some(input => input.sources?.length))) throw new Error("Choose a binding with collected-source inputs.");
  const result = Object.fromEntries(ids.map(id => {
    const binding = bindings.find(value => value.id === id)!;
    const text = values[id] || budgetText(binding.budget);
    const budget = Object.fromEntries(budgetFields.map(key => {
      const value = Number(text[key]);
      if (!/^\d+$/.test(text[key]) || !Number.isSafeInteger(value) || value < (key === "max_cost_micros" ? 0 : 1) || value > binding.budget[key]) throw new Error("Every budget must be a whole number within the binding ceiling.");
      return [key, value];
    })) as Budget;
    return [id, budget];
  }));
  for (const field of ["max_tokens", "max_cost_micros"] as const) {
    const total = Object.values(result).reduce((sum, budget) => sum + budget[field], 0);
    if (!Number.isSafeInteger(total)) throw new Error("Combined budgets exceed the browser’s exact-integer range. Reduce the budgets.");
  }
  return result;
}
export function backfillInterval(from: string, until: string): { from: string; until: string } {
  const first = new Date(from), last = new Date(until);
  if (!Number.isFinite(+first) || !Number.isFinite(+last) || +last <= +first || +last - +first > 90 * 86400000) throw new Error("Choose a time range of at most 90 days.");
  return { from: first.toISOString(), until: last.toISOString() };
}
export function localInputTime(date: Date) { return new Date(+date - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16); }
