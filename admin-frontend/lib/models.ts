import { intlLocale, currentLocale } from "@web/i18n/locale";
import type { Identity } from "@web/identity";
export type Group = { id: string; name: string; revision: number };
export type User = Identity & { created_at: string; updated_at: string; last_login_at?: string; groups: Group[] };
export type UserPage = { users: User[]; next_cursor?: string };
export type Operation = { id: string; action: string; target_kind: string; target_id: string; state: "accepted" | "running" | "succeeded" | "failed" | "unknown"; error?: string; total_targets: number; completed_targets: number; created_at: string; updated_at: string };
export type Mutation = { user: User; operation?: Operation; replayed: boolean; temporary_password?: string };
export type AuditEvent = { id: number; actor_id: string; action: string; target_kind: string; target_id: string; request_id?: string; revision: number; details: Record<string, unknown>; created_at: string };
export function date(value?: string) { return value ? new Date(value).toLocaleString(intlLocale()) : currentLocale() === "zh" ? "从未" : "Never"; }
