"use client";
import { formatQuantity } from "@web/i18n/locale";
import { useT } from "@/lib/i18n";

import { resourceDimensions, type FitIssue, type PlanConflicts, type UserUsage } from "@web/resources";
export function FitIssues({ issues }: { issues: FitIssue[] }) {
  const tr = useT();
  return issues.length ? <ul>{issues.map((issue, index) => <li key={index}>{tr(issue.dimension || issue.field || issue.code)}: {issue.reason ? tr(issue.reason) : tr("{requested} requested, {limit} allowed", { requested: formatQuantity(issue.requested), limit: formatQuantity(issue.limit) })}{issue.field && ` (${issue.field})`}</li>)}</ul> : <p>{tr("No conflicts.")}</p>;
}
export function SavedPlanIssues({ value }: { value: PlanConflicts }) {
  const tr = useT();
  return <><p>{value.count}  {tr("saved Projects conflict with these limits. Existing data is retained.")}</p>{value.projects.map(project => <details key={project.project_id}><summary>{project.project_id} · {tr(project.state)}</summary><FitIssues issues={project.issues} /></details>)}{value.truncated && <p>{tr("Showing the first 20 conflicting Projects.")}</p>}</>;
}
export function ResourceUsageView({ value }: { value: UserUsage }) {
  const tr = useT();
  return <section className="panel"><h2>{tr("Effective resources and usage")}</h2><p>{tr("Whole integer units. Bytes are capacity, including owned copies. Shared publications are not charged to the user. Usage includes startup reservations and unsettled allocations.")}</p>
    <div className="table-scroll"><table><thead><tr><th>{tr("Resource")}</th><th>{tr("Inherited")}</th><th>{tr("Override")}</th><th>{tr("Effective / ceiling")}</th><th>{tr("Used / reserved")}</th></tr></thead><tbody>{resourceDimensions.map(([key, label, unit]) => {
      const field = value.settings.effective[key], usage = value.usage[key];
      return <tr key={key}><td>{tr(label)}<small>{tr(unit)}</small><details><summary>{tr("Sources")}</summary>{field.sources.length ? field.sources.map(source => <p key={source.group_id}>{source.group_name}{tr(": default")} {formatQuantity(source.default)}{tr(", ceiling")} {formatQuantity(source.ceiling)}</p>) : <p>{tr("No group entitlement.")}</p>}<p>{tr("Platform maximum:")} {field.platform_limit}</p></details></td><td>{formatQuantity(field.default)}</td><td>{field.override ?? tr("Inherit")}{field.capped && <strong>  {tr("(capped)")}</strong>}</td><td>{formatQuantity(field.effective)} / {formatQuantity(field.ceiling)}</td><td>{usage ? `${usage.used} / ${usage.reserved}` : tr("Per Project or request limit")}</td></tr>;
    })}</tbody></table></div>
    <details><summary>{tr("Current usage overages (")}{value.overages.length})</summary><FitIssues issues={value.overages} /></details>
    <details><summary>{tr("Current default environment fit (")}{value.default_plan_issues.length})</summary><FitIssues issues={value.default_plan_issues} /></details>
    <details><summary>{tr("Saved Project conflicts (")}{value.saved_plan_conflicts.count})</summary><SavedPlanIssues value={value.saved_plan_conflicts} /></details>
  </section>;
}
