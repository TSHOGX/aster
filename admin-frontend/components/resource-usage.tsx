"use client";
import { resourceDimensions, type FitIssue, type PlanConflicts, type UserUsage } from "@web/resources";
export function FitIssues({ issues }: { issues: FitIssue[] }) {
  return issues.length ? <ul>{issues.map((issue, index) => <li key={index}>{issue.dimension || issue.field || issue.code}: {issue.requested} requested, {issue.limit} allowed{issue.field && ` (${issue.field})`}</li>)}</ul> : <p>No conflicts.</p>;
}
export function SavedPlanIssues({ value }: { value: PlanConflicts }) {
  return <><p>{value.count} saved Projects conflict with these limits. Existing data is retained.</p>{value.projects.map(project => <details key={project.project_id}><summary>{project.project_id} · {project.state}</summary><FitIssues issues={project.issues} /></details>)}{value.truncated && <p>Showing the first 20 conflicting Projects.</p>}</>;
}
export function ResourceUsageView({ value }: { value: UserUsage }) {
  return <section className="panel"><h2>Effective resources and usage</h2><p>Whole integer units. Bytes are capacity, including owned copies. Shared publications are not charged to the user. Usage includes startup reservations and unsettled allocations.</p>
    <div className="table-scroll"><table><thead><tr><th>Resource</th><th>Inherited</th><th>Override</th><th>Effective / ceiling</th><th>Used / reserved</th></tr></thead><tbody>{resourceDimensions.map(([key, label, unit]) => {
      const field = value.settings.effective[key], usage = value.usage[key];
      return <tr key={key}><td>{label}<small>{unit}</small><details><summary>Sources</summary>{field.sources.length ? field.sources.map(source => <p key={source.group_id}>{source.group_name}: default {source.default}, ceiling {source.ceiling}</p>) : <p>No group entitlement.</p>}<p>Platform maximum: {field.platform_limit}</p></details></td><td>{field.default}</td><td>{field.override ?? "Inherit"}{field.capped && <strong> (capped)</strong>}</td><td>{field.effective} / {field.ceiling}</td><td>{usage ? `${usage.used} / ${usage.reserved}` : "Per Project or request limit"}</td></tr>;
    })}</tbody></table></div>
    <details><summary>Current usage overages ({value.overages.length})</summary><FitIssues issues={value.overages} /></details>
    <details><summary>Current default environment fit ({value.default_plan_issues.length})</summary><FitIssues issues={value.default_plan_issues} /></details>
    <details><summary>Saved Project conflicts ({value.saved_plan_conflicts.count})</summary><SavedPlanIssues value={value.saved_plan_conflicts} /></details>
  </section>;
}
