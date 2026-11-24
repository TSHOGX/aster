// Review, conflict, publication and history pins identify one branch's
// evidence. They must be dropped when the working branch changes and kept
// when the same branch is selected again.
const branchEvidenceParams = ["integration", "conflict", "publication", "history_head"] as const;

export function applyBranchSelection(url: URL, previous: string | undefined, next: string) {
  url.searchParams.set("branch", next);
  if (next !== previous) {
    for (const key of branchEvidenceParams) url.searchParams.delete(key);
  }
  url.searchParams.delete("version");
}
