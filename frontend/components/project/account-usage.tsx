"use client";
import { useEffect, useState } from "react";
import { api } from "@/lib/project-api";
import { useT } from "@/lib/i18n";
import { resourceDimensions, type FitIssue, type UserUsage } from "@web/resources";

export function AccountUsage() {
  const tr = useT();
  const [value, setValue] = useState<UserUsage | null>(null), [error, setError] = useState(""), [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    void api<UserUsage>("/api/me/usage", { signal: abort.signal }).then(value => { if (!abort.signal.aborted) setValue(value); }).catch(error => { if (!abort.signal.aborted) setError(String(error)); });
    return () => abort.abort();
  }, [attempt]);
  const issues = (items: FitIssue[]) => <ul>{items.map((issue, index) => { const dimension = resourceDimensions.find(([key]) => key === issue.dimension); return <li key={index}>{dimension ? tr(dimension[1]) : tr("Environment configuration")}: {issue.reason || tr("Requested {requested}; allowed {limit}", { requested: issue.requested, limit: issue.limit })}</li>; })}</ul>;
  return <div className="account-usage">
    <p>{tr("Your administrator manages these limits. Existing data is retained after reductions; new allocations and resume must fit current limits.")}</p>
    {error && <p className="settings-error" role="alert">{error}</p>}
    {!value && !error && <p role="status">{tr("Loading resources…")}</p>}
    <button onClick={() => { setError(""); setValue(null); setAttempt(value => value + 1); }}>{tr("Refresh resources")}</button>
    {value && <>
      {resourceDimensions.map(([dimension, label, unit]) => {
        const field = value.settings.effective[dimension], usage = value.usage[dimension];
        return <details className="resource-allowance" key={dimension}><summary>{tr(label)}: {field.effective} {tr(unit)}{field.capped && ` (${tr("Capped")})`}</summary>
          <dl><dt>{tr("Inherited default")}</dt><dd>{field.default}</dd><dt>{tr("Requested override")}</dt><dd>{field.override ?? tr("Inherit")}</dd><dt>{tr("Effective ceiling")}</dt><dd>{field.ceiling}</dd><dt>{tr("Platform maximum")}</dt><dd>{field.platform_limit}</dd>
            {usage && <><dt>{tr("Used")}</dt><dd>{usage.used}</dd><dt>{tr("Reserved")}</dt><dd>{usage.reserved}</dd></>}
          </dl>
          {field.capped && <p>{tr("The saved request is capped. It may become effective again if your entitlement increases.")}</p>}
          {usage ? <p>{tr("Usage includes startup reservations and unsettled allocations. Storage measures capacity, not file sizes.")}</p> : <p>{tr("This is a per-Project or per-request allowance.")}</p>}
          <p>{tr("Group sources")}</p>{field.sources.length ? <ul>{field.sources.map(source => <li key={source.group_id}>{source.group_name}: {tr("Default {default}; ceiling {ceiling}", { default: source.default, ceiling: source.ceiling })}</li>)}</ul> : <p>{tr("No group entitlement.")}</p>}
        </details>;
      })}
      <details><summary>{tr("Usage overages")} ({value.overages.length})</summary>{issues(value.overages)}</details>
      <details><summary>{tr("Default environment conflicts")} ({value.default_plan_issues.length})</summary>{issues(value.default_plan_issues)}</details>
      <details><summary>{tr("Saved Project conflicts")} ({value.saved_plan_conflicts.count})</summary>{value.saved_plan_conflicts.projects.map(project => <div key={project.project_id}><p>{project.project_id}</p>{issues(project.issues)}</div>)}{value.saved_plan_conflicts.truncated && <p>{tr("Showing the first 20 conflicting Projects.")}</p>}</details>
      <details><summary>{tr("Network policy")}: {value.network.mode === "inherit" ? tr("Inherit") : tr("Complete override")}</summary><p>{tr("These rules apply on the next creation or resume. Running Projects retain their current policy.")}</p><ul>{value.network.desired.direct.map(rule => <li key={`${rule.cidr}-${rule.port}-${rule.protocol}`}>{rule.cidr} · {rule.port}/{rule.protocol.toUpperCase()}</li>)}</ul><p>{tr("DNS resolvers")}: {value.network.desired.resolvers.join(", ") || tr("None")}</p></details>
    </>}
  </div>;
}
