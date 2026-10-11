import type { EffectiveResource, FitIssue, PlanConflicts, ResourceDimension, Usage } from "@web/resources";
export type Artifact = { id: string; version: string; digest: string };
export type RequiredPublication = { role: string; destination: string; artifact_id: string };
export type Mount = { key: string; role: string; destination: string; storage: "owned" | "shared_readonly"; access: "read_write" | "read_only"; source: { kind: "empty" | "builtin"; artifact_id?: string; version?: string; digest?: string }; capacity: { setting?: "workspace_bytes" | "runtime_bytes"; bytes?: string } };
export type EnvironmentBody = { sandbox_release: Artifact; mounts: Mount[] };
export type Environment = EnvironmentBody & { revision: number; required_publications: RequiredPublication[]; max_mounts: string };
export type RuntimeOption = { kind: "sandbox" | "volume"; reference: Artifact; enabled: boolean; availability: string; checked_at?: string; required_publications?: RequiredPublication[]; protected_roots?: string[]; blocked_path_count?: number; roles?: string[]; minimum_bytes?: string; source_capacity_bytes?: string; clean_runtime?: boolean };
export type OptionsPage = { options: RuntimeOption[]; max_mounts: string; next_cursor?: string };
export type EnvironmentPreview = Environment & { expected_dependencies: string; affected_accounts: string; accounts_with_plan_conflicts: string; next_cursor?: string; users: { account_id: string; username: string; effective: Record<ResourceDimension, EffectiveResource>; plan: EnvironmentBody & { mount_plan: (Mount & { capacity_bytes: string })[]; owned_bytes: string }; issues: FitIssue[]; usage: Usage; overages: FitIssue[]; saved_plan_conflicts: PlanConflicts }[] };
export const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
export const artifactKey = (value: Artifact) => JSON.stringify([value.id, value.version, value.digest]);
export const bodyOf = (value: Environment): EnvironmentBody => ({ sandbox_release: value.sandbox_release, mounts: value.mounts });
// Preserve the user's changed fields while accepting unrelated concurrent edits.
// Stable slot keys make additions/removals independent of array position.
export function mergeEnvironment(base: EnvironmentBody, draft: EnvironmentBody, latest: EnvironmentBody): EnvironmentBody {
  const before = new Map(base.mounts.map(mount => [mount.key, mount])), edited = new Map(draft.mounts.map(mount => [mount.key, mount]));
  const mounts: Mount[] = [];
  for (const remote of latest.mounts) {
    const old = before.get(remote.key), local = edited.get(remote.key);
    if (old && !local) continue;
    if (!old || !local) { mounts.push(remote); continue; }
    mounts.push({ key: remote.key, role: local.role === old.role ? remote.role : local.role, destination: local.destination === old.destination ? remote.destination : local.destination, storage: local.storage === old.storage ? remote.storage : local.storage, access: local.access === old.access ? remote.access : local.access, source: same(local.source, old.source) ? remote.source : local.source, capacity: same(local.capacity, old.capacity) ? remote.capacity : local.capacity });
  }
  for (const mount of draft.mounts) if (!latest.mounts.some(remote => remote.key === mount.key) && (!before.has(mount.key) || !same(mount, before.get(mount.key)))) mounts.push(mount);
  return { sandbox_release: same(base.sandbox_release, draft.sandbox_release) ? latest.sandbox_release : draft.sandbox_release, mounts };
}
