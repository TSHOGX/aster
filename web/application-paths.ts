const groupQueryKeys = ["groups_search", "groups_after", "runs_after", "runs_state", "runs_template", "runs_workflow", "runs_workflow_revision", "runs_base_commit", "runs_source", "runs_author", "runs_from", "runs_until", "evaluations_after"];

// Carry only list context, never an arbitrary return URL or detail selection.
export function evolutionContextQuery(search: { get(name: string): string | null }): string {
  const query = new URLSearchParams();
  for (const key of groupQueryKeys) {
    const value = search.get(key);
    if (value && value.length <= 256) query.set(key, value);
  }
  return query.size ? `?${query}` : "";
}
export function evolutionGroupPath(group: string, search: { get(name: string): string | null }): string {
  return `/groups/${encodeURIComponent(group)}/${evolutionContextQuery(search)}`;
}
// Detail pages return to their section list with the same bounded list context.
export function evolutionSectionPath(group: string, section: string, search: { get(name: string): string | null }): string {
  return `/groups/${encodeURIComponent(group)}/${section}/${evolutionContextQuery(search)}`;
}
export function studioBranchPath(library: string, branch: string): string {
  return `/volumes-studio/libraries/${encodeURIComponent(library)}/?${new URLSearchParams({ branch })}`;
}
