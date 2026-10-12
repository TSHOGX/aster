export const productActions = [
  "projects.read", "projects.create", "projects.manage",
  "files.list", "files.read", "files.export", "files.upload", "files.edit", "files.manage",
  "conversations.share", "shares.public.create", "terminals.use", "agents.run",
  "schedules.manage", "admin.content.read",
] as const;
export type ProductAction = typeof productActions[number];
export type EffectiveAccess = {
  account_id: string;
  account_status: string;
  management_role: string;
  revision: number;
  actions: Partial<Record<ProductAction, { allowed: boolean }>>;
};
