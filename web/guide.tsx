"use client";
import type { ReactNode } from "react";

// One step of an area's workflow. `href` makes the step a link; `state` marks
// progress. Text is already translated by the caller so each app keeps its own
// catalog and routing (Next Link or plain anchors).
export type GuideStep = {
  id: string;
  title: ReactNode;
  description?: ReactNode;
  href?: string;
  state?: "done" | "current" | "todo";
  link?: (props: { href: string; className: string; children: ReactNode }) => ReactNode;
};

const anchor = ({ href, className, children }: { href: string; className: string; children: ReactNode }) => <a href={href} className={className}>{children}</a>;

// Guide explains how the pieces of one area fit together, in order. The full
// form suits overviews; `compact` is a one-line breadcrumb for section pages
// that shows where the page sits in the same flow.
export function Guide({ title, description, steps, note, compact, current, label, columns }: {
  title?: ReactNode; description?: ReactNode; steps: GuideStep[]; note?: ReactNode; compact?: boolean; current?: string; label: string; columns?: number;
}) {
  return <section className={compact ? "guide compact" : "guide"} aria-label={label} style={columns ? { ["--guide-columns" as string]: String(columns) } : undefined}>
    {!compact && (title || description) && <header>{title && <h2>{title}</h2>}{description && <p>{description}</p>}</header>}
    <ol className="guide-steps">{steps.map((step, index) => {
      const active = step.id === current || step.state === "current";
      const body = <><span className="guide-title"><span className="guide-index" aria-hidden="true">{step.state === "done" ? "✓" : index + 1}</span><strong>{step.title}</strong></span>{step.description && <small>{step.description}</small>}</>;
      const render = step.link ?? anchor;
      return <li key={step.id} aria-current={active ? "step" : undefined} data-state={step.state}>
        {step.href ? render({ href: step.href, className: "guide-step", children: body }) : <div className="guide-step">{body}</div>}
      </li>;
    })}</ol>
    {note && !compact && <p className="guide-note">{note}</p>}
  </section>;
}

// EmptyState names what is missing and offers the next action.
export function EmptyState({ title, children, actions, heading = "h2" }: { title: ReactNode; children?: ReactNode; actions?: ReactNode; heading?: "h2" | "h3" }) {
  const Heading = heading;
  return <div className="empty-panel"><Heading>{title}</Heading>{children && <p>{children}</p>}{actions && <div className="button-row">{actions}</div>}</div>;
}

export type BadgeTone = "neutral" | "success" | "warning" | "danger" | "info";
export function StateBadge({ tone = "neutral", children }: { tone?: BadgeTone; children: ReactNode }) {
  return <span className={`state-badge ${tone === "neutral" ? "" : tone}`.trim()}>{children}</span>;
}

export function Callout({ tone = "info", title, children, actions, role }: { tone?: "info" | "warning" | "danger"; title?: ReactNode; children?: ReactNode; actions?: ReactNode; role?: "alert" | "status" }) {
  return <div className={`callout ${tone}`} role={role}>{title && <strong>{title}</strong>}{children && <p>{children}</p>}{actions && <div className="button-row">{actions}</div>}</div>;
}
