import { api, type Entry, type Library, type Version } from "./api";

export type ContentRef = {
  library_id: string;
  commit_id: string;
  profile: Library["profile"];
  tree_digest: string;
};
export type Branch = {
  id: string;
  library_id: string;
  name: string;
  initial_base: string;
  head: string;
  draft_revision: number;
};
export type Checkpoint = {
  id: string;
  branch_id: string;
  old_head: string;
  commit_id: string;
  draft_revision: number;
};
export type PreparedIntegration = {
  conflict_workspace_id?: string;
  digest: string;
  identity?: {
    library_id: string;
    main: ContentRef;
    sources: ContentRef[];
    selections?: { branch_id: string; head: string; selected_paths: string[] }[];
    result: ContentRef;
    policy_revision: number;
    policy_digest: string;
    checks: {digest:string;policy_id:string;policy_revision:number;path:string;mode:"required"|"report_only";profile:{id:string;revision:number};service:{id:string;revision:number};reference_strategy:string;reference?:ContentRef}[];
  };
  conflicts: { path: string; reason: string }[];
};
export type BranchChange = { path: string; change: string };
export type BranchChangePage = { changes: BranchChange[]; next: string; total: number; revision: number; head: string };

const libraryRoot = (library: string) => `/libraries/${encodeURIComponent(library)}`;
export const branchRoot = (library: string, branch: string) =>
  `${libraryRoot(library)}/branches/${encodeURIComponent(branch)}`;

// Callers retain request IDs across retries and refresh reconciliation. A failed
// request must not silently acquire a new identity inside this transport layer.
export const branchAPI = {
  entry: (library: string, branch: string, path: string, revision = 0, signal?: AbortSignal) =>
    api<{ branch_id: string; head: string; revision: number; entry: Entry | null }>(`${branchRoot(library, branch)}/draft/entry?${new URLSearchParams({ path, revision: String(revision) })}`, { signal }),
  list: (library: string, after = "", signal?: AbortSignal) =>
    api<{ branches: Branch[]; next: string }>(`${libraryRoot(library)}/branches?${new URLSearchParams({ after })}`, { signal }),
  main: (library: string, signal?: AbortSignal) =>
    api<ContentRef>(`${libraryRoot(library)}/main`, { signal }),
  read: (library: string, branch: string, signal?: AbortSignal) =>
    api<Branch>(branchRoot(library, branch), { signal }),
  create: (library: string, name: string, base: string, request: string, csrf: string) =>
    api<Branch>(`${libraryRoot(library)}/branches`, {
      method: "POST", body: JSON.stringify({ request_id: request, name, base_commit: base }),
    }, csrf),
  tree: (branch: Branch, parent = "", after = "", signal?: AbortSignal) =>
    api<{ revision: number; entries: Entry[]; next?: string; descendant_count: number }>(
      `${branchRoot(branch.library_id, branch.id)}/draft/tree?${new URLSearchParams({ parent, after, revision: String(branch.draft_revision) })}`, { signal }),
  headDiff: (branch: Branch, after = "", signal?: AbortSignal) =>
    api<BranchChangePage>(`${branchRoot(branch.library_id, branch.id)}/head-diff?${new URLSearchParams({ head: branch.head, after })}`, { signal }),
  saveText: (branch: Branch, path: string, text: string, request: string, csrf: string) =>
    api<{ revision: number; affected: number }>(`${branchRoot(branch.library_id, branch.id)}/draft/text`, {
      method: "POST", body: JSON.stringify({ request_id: request, expected_revision: branch.draft_revision, expected_head: branch.head, path, text }),
    }, csrf),
  checkpoint: (branch: Branch, message: string, request: string, csrf: string) =>
    api<Checkpoint>(`${branchRoot(branch.library_id, branch.id)}/commits`, {
      method: "POST", body: JSON.stringify({ request_id: request, expected_revision: branch.draft_revision, expected_head: branch.head, message }),
    }, csrf),
  prepare: (branch: Branch, main: ContentRef, policyRevision: number, request: string, csrf: string, rebindPreparedDigest = "") =>
    api<PreparedIntegration>(`${libraryRoot(branch.library_id)}/integrations/prepare`, {
      method: "POST", body: JSON.stringify({ request_id: request, branch_id: branch.id, expected_head: branch.head, expected_main: main.commit_id, expected_policy_revision: policyRevision, ...(rebindPreparedDigest ? { rebind_prepared_digest: rebindPreparedDigest } : {}) }),
    }, csrf),
  compose: (library: string, expectedMain: string, policyRevision: number, branches: { branch_id: string; expected_head: string; selected_paths: string[] }[], request: string, csrf: string) =>
    api<PreparedIntegration & { selections: { branch_id: string; head: string; selected_paths: string[]; deferred_paths?: string[] }[] }>(`${libraryRoot(library)}/integrations/compose`, {
      method: "POST", body: JSON.stringify({ request_id: request, expected_main: expectedMain, expected_policy_revision: policyRevision, branches }),
    }, csrf),
  review: (library: string, digest: string, verdict: "approve" | "changes_requested" | "reject", rationale: string, request: string, csrf: string) =>
    api(`${libraryRoot(library)}/integrations/reviews`, {
      method: "POST", body: JSON.stringify({ request_id: request, prepared_digest: digest, verdict, rationale }),
    }, csrf),
  merge: (library: string, digest: string, expectedMain: string, request: string, csrf: string) =>
    api<{ operation_id: string; accepted_commit: string; prepared_digest: string }>(`${libraryRoot(library)}/integrations/merge`, {
      method: "POST", body: JSON.stringify({ request_id: request, prepared_digest: digest, expected_main: expectedMain }),
    }, csrf),
  release: (library: string, commit: string, label: string, note: string, request: string, csrf: string) =>
    api<Version>(`${libraryRoot(library)}/versions`, {
      method: "POST", body: JSON.stringify({ request_id: request, accepted_commit: commit, label, note }),
    }, csrf),
};

export type ConflictWorkspace = {
  id: string; library_id: string; branch_id: string; base_commit: string;
  main_commit: string; source_commit: string; policy_revision: number;
  revision: number; state: "open" | "resolved"; stale: boolean;
  conflicts: { path: string; reason: string }[];
  resolutions: { path: string; choice: "main" | "branch" | "delete" | "text"; text: string }[];
};
export const conflictAPI = {
  read: (library: string, workspace: string, signal?: AbortSignal) =>
    api<ConflictWorkspace>(`${libraryRoot(library)}/conflicts/${encodeURIComponent(workspace)}`, { signal }),
};
