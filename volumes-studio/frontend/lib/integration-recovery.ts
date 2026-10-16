// Only these operations may be replayed; storage never supplies a URL or method.
export type IntegrationAction = "prepare" | "compose" | "review" | "merge" | "evaluation" | "publish";
export type IntegrationCommand = { library: string; branch: string; action: IntegrationAction; digest: string; body: string };
export function integrationEndpoint(command: IntegrationCommand): string {
  switch (command.action) {
    case "prepare": return "/integrations/prepare";
    case "compose": return "/integrations/compose";
    case "review": return "/integrations/reviews";
    case "merge": return "/integrations/merge";
    case "evaluation": return `/integrations/${encodeURIComponent(command.digest)}/evaluations`;
    case "publish": return "/versions";
  }
}
export function readIntegrationCommand(raw: string, library: string, branch: string): IntegrationCommand {
  if (raw.length > 2 << 20) throw new Error("oversized recovery");
  const c = JSON.parse(raw) as IntegrationCommand;
  if (c?.library !== library || c.branch !== branch || typeof c.body !== "string" || typeof c.digest !== "string" || !["prepare", "compose", "review", "merge", "evaluation", "publish"].includes(c.action)) throw new Error("invalid recovery scope");
  const body = JSON.parse(c.body);
  const id = (value: unknown) => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);
  const digest = (value: unknown) => typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
  const revision = (value: unknown) => Number.isSafeInteger(value) && Number(value) > 0;
  const string = (value: unknown) => typeof value === "string";
  if (!body || !id(body.request_id)) throw new Error("invalid request identity");
  let valid = false;
  switch (c.action) {
    case "prepare": valid = body.branch_id === branch && id(body.expected_head) && id(body.expected_main) && revision(body.expected_policy_revision) && (c.digest === "" ? !body.rebind_prepared_digest : digest(c.digest) && body.rebind_prepared_digest === c.digest); break;
    case "compose": valid = id(body.expected_main) && revision(body.expected_policy_revision) && Array.isArray(body.branches) && body.branches.length > 0 && body.branches.every((b: {branch_id: string; expected_head: string; selected_paths: string[]}) => b && id(b.branch_id) && id(b.expected_head) && Array.isArray(b.selected_paths) && b.selected_paths.length > 0 && b.selected_paths.every(string)); break;
    case "review": valid = body.prepared_digest === c.digest && ["approve", "changes_requested", "reject"].includes(body.verdict) && string(body.rationale); break;
    case "merge": valid = body.prepared_digest === c.digest && id(body.expected_main); break;
    case "evaluation": valid = digest(body.requirement_digest) && body.budget && ["max_tokens", "max_cost_micros", "max_attempts", "timeout_seconds", "max_artifact_bytes"].every(key => Number.isSafeInteger(body.budget[key]) && body.budget[key] >= 0) && Number.isSafeInteger(body.seed); break;
    case "publish": valid = id(body.accepted_commit) && string(body.label) && string(body.note); break;
  }
  if (!valid || (!["prepare", "compose"].includes(c.action) && !digest(c.digest))) throw new Error("invalid operation body");
  return { library, branch, action: c.action, digest: c.digest, body: c.body };
}
