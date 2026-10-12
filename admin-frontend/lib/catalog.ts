import type { TemplateRef } from "@web/configuration";
import type { EnvironmentBody } from "./runtime-models";
export type Network = { revision?: string; direct: { cidr: string; port: number; protocol: "tcp" | "udp" }[]; resolvers: string[] };
export type Template = TemplateRef & { kind: "network" | "environment"; revision: number; builtin: boolean; network?: Network; environment?: EnvironmentBody & { revision: number }; usage_count: number; created_at: string; updated_at: string };
export type SharedVolume = TemplateRef & { revision: number; capacity: string; state: string; error?: string; updated_at: string };
export type Impact = { expected_dependencies: string; proposed: unknown; users: { id: string; name: string; needs_admin_resolution: boolean }[]; projects: { id: string; title: string; state: string; suspended: boolean; reason: string }[] };
