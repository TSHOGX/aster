"use client";
import Link from "next/link";
import type { ReactNode } from "react";
import { Guide, type GuideStep } from "@web/guide";
import { useT } from "../lib/i18n";
import { useGroup } from "./group-context";

export type FlowStep = "workflows" | "templates" | "bindings" | "runs";
const link = ({ href, className, children }: { href: string; className: string; children: ReactNode }) => <Link prefetch={false} href={href} className={className}>{children}</Link>;

// The four steps from a workflow to a reviewed result. The overview shows them
// in full; each section page shows a compact line marking where it sits.
function useSteps(): GuideStep[] {
  const tr = useT(); const { group } = useGroup();
  const root = `/groups/${encodeURIComponent(group.id)}/`;
  return [
    { id: "workflows", href: `${root}workflows/`, link, title: tr("Workflow"), description: tr("How the work runs: a graph of agents and checks with typed inputs and outputs. Start from a built-in example.") },
    { id: "templates", href: `${root}templates/`, link, title: tr("Goal template"), description: tr("What you want to produce: the goal, compatible workflows, output format and a suggested directory.") },
    { id: "bindings", href: `${root}settings/configuration/#bindings`, link, title: tr("Configuration binding"), description: tr("Where it applies: sources to read, the Volume and directory to write, trigger, budget and evaluation mode.") },
    { id: "runs", href: `${root}runs/`, link, title: tr("Run and review"), description: tr("Each run writes a branch in Volume Studio. An expert reviews and merges it; publishing is a separate step.") },
  ];
}

export function FlowOverview({ children }: { children?: ReactNode }) {
  const tr = useT(); const { group } = useGroup();
  const root = `/groups/${encodeURIComponent(group.id)}/`;
  return <Guide label={tr("How the pieces fit together")} title={tr("How the pieces fit together")} steps={useSteps()}
    note={<><Link prefetch={false} href={`${root}profiles/`}>{tr("Evaluation profile")}</Link> · {tr("Optional. Measures a candidate on a held-out dataset before review. A binding sets it to Off, Informational or Required for merge.")}{children}</>} />;
}

// FlowPosition is the compact form for section pages.
export function FlowPosition({ step }: { step: FlowStep }) {
  const tr = useT();
  return <Guide compact label={tr("Where this page sits")} steps={useSteps()} current={step} />;
}
