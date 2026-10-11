export const resourceDimensions = [
  ["projects", "Projects", "count"], ["running_projects", "Running Projects", "count"],
  ["cpu_millis", "CPU per Project", "millis"], ["memory_bytes", "Memory per Project", "bytes"],
  ["overlay_bytes", "Overlay per Project", "bytes"], ["workspace_bytes", "Workspace per Project", "bytes"],
  ["runtime_bytes", "Runtime per Project", "bytes"], ["aggregate_cpu_millis", "Total CPU", "millis"],
  ["aggregate_memory_bytes", "Total memory", "bytes"], ["owned_volume_bytes", "Maximum owned Volume", "bytes"],
  ["project_owned_volume_bytes", "Owned storage per Project", "bytes"], ["mount_count", "Mounts per Project", "count"],
  ["persistent_bytes", "Persistent storage", "bytes"], ["agents", "Concurrent agents", "count"],
  ["terminals", "Concurrent terminals", "count"], ["schedules", "Active schedules", "count"],
  ["transfer_bytes", "File transfer per request", "bytes"],
] as const;
export type ResourceDimension = typeof resourceDimensions[number][0];
export type Entitlement = { default: string; ceiling: string };
export type EffectiveResource = Entitlement & { platform_limit: string; override: string | null; effective: string; capped: boolean; sources: (Entitlement & { group_id: string; group_name: string })[] };
export type ResourceSettings = { revision: number; account_id: string; overrides: Partial<Record<ResourceDimension, string | null>>; effective: Record<ResourceDimension, EffectiveResource> };
export type GroupResources = { revision: number; group_id: string; dimensions: Partial<Record<ResourceDimension, Entitlement>> };
export type Usage = Partial<Record<ResourceDimension, { used: string; reserved: string }>>;
export type FitIssue = { code: string; reason?: string; dimension?: ResourceDimension; requested: string; limit: string; field?: string };
export type PlanConflicts = { count: string; projects: { project_id: string; state: string; issues: FitIssue[] }[]; truncated: boolean };
export type UserUsage = { settings: ResourceSettings; usage: Usage; overages: FitIssue[]; default_plan_issues: FitIssue[]; saved_plan_conflicts: PlanConflicts; network: { mode: string; desired: { revision: string; direct: { cidr: string; port: number; protocol: string }[]; resolvers: string[] } } };
export type ResourceImpact = { account_id: string; username: string; before: Record<ResourceDimension, EffectiveResource>; after: Record<ResourceDimension, EffectiveResource>; usage: Usage; before_overages: FitIssue[]; after_overages: FitIssue[]; before_plan_issues: FitIssue[]; after_plan_issues: FitIssue[]; before_saved_plan_conflicts: PlanConflicts; after_saved_plan_conflicts: PlanConflicts };
export type ResourcePreview = { expected_dependencies: string; affected_accounts: string; users: ResourceImpact[]; next_cursor?: string };
export const validResourceInteger = (value: string) => /^(0|[1-9][0-9]*)$/.test(value) && value.length <= 19 && BigInt(value) <= BigInt("9223372036854775807");
