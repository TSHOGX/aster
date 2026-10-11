import type { Artifact } from "./runtime-models";
export type AdminProject = { execution_suspended: boolean; id: string; title: string; owner_id: string; owner_username: string; owner_status: string; state: string; revision: number; updated_at: string; bound: boolean; environment_revision: number; sandbox_release: Artifact | null; allocated_bytes: string; reserved_bytes: string; slot_count: string };
export type AdminProjectPage = { projects: AdminProject[]; next_cursor?: string };
export type VolumeSlot = { key: string; role: string; destination: string; storage: string; access: string; capacity_bytes: string; source?: Artifact; source_minimum_bytes: string; clean_runtime: boolean; volume_id?: string; state: string; error?: string; allocated_bytes: string; reserved_bytes: string; observation: string; observed_capacity_bytes?: string; checked_at?: string };
export type ProjectDetail = AdminProject & {
  failure?: string;
  binding: null | { resource_revision: number; store_id: string; template_id: string; sandbox_id?: string; epoch: string; occupied: boolean; cpu_millis: string; memory_bytes: string; overlay_bytes: string; slots: VolumeSlot[] };
  desired_network: { revision: string; direct: { cidr: string; port: number; protocol: string }[]; resolvers: string[] };
  network_mode: string; saved_network_revision?: string; network_pending: boolean;
  observation: { status: string; checked_at?: string; state?: string; placement_state?: string; matches_saved_binding?: boolean; network_revision?: string };
  operation: null | { id: string; kind: string; state: string; phase: string; error?: string; created_at: string; attempted_at: string; network_revision?: string };
  admin_operations: string[];
};
