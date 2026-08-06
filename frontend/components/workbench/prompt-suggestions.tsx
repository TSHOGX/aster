"use client";

import { Activity, FlaskConical, LineChart, Thermometer } from "lucide-react";

import { Card } from "@/components/ui/card";
import { requestComposerDraft } from "@/lib/composer-draft";

/**
 * Starting points offered on an empty session.
 *
 * Deliberately domain-specific: a generic "ask me anything" tells a new user
 * nothing about what this workbench is for, whereas these name the artifacts the
 * agent actually works with (cycling data, dQ/dV curves, thermal logs). Picking
 * one fills the composer rather than sending, since every prompt needs the user's
 * own file or cell name substituted in.
 */
const SUGGESTIONS = [
  {
    Icon: LineChart,
    title: "Capacity fade analysis",
    body: "Plot capacity retention against cycle number, fit the fade curve, and report the projected cycle count at 80% state of health.",
    prompt:
      "I have cycling data in /workspace. Load it, plot capacity retention vs. cycle number, fit the fade trend, and estimate the cycle count at 80% SOH."
  },
  {
    Icon: Activity,
    title: "dQ/dV peak tracking",
    body: "Compute differential capacity curves across cycles and track how the peaks shift to separate loss of lithium inventory from loss of active material.",
    prompt:
      "Compute dQ/dV curves from the charge data in /workspace, plot them by cycle, and track the peak positions to identify the dominant degradation mode."
  },
  {
    Icon: Thermometer,
    title: "Thermal log review",
    body: "Scan a temperature log for excursions, correlate them with C-rate, and summarize which steps ran hottest.",
    prompt:
      "Review the thermal log in /workspace: find temperature excursions, correlate them with C-rate and step type, and summarize the hottest steps in a table."
  },
  {
    Icon: FlaskConical,
    title: "Compare cell variants",
    body: "Put several cells or formulations side by side on the metrics that matter and call out where they diverge.",
    prompt:
      "Compare the cells in /workspace on capacity, coulombic efficiency, and internal resistance. Produce a comparison table and flag where the variants diverge."
  }
] as const;

export function PromptSuggestions() {
  return (
    <div className="grid w-full gap-2.5 sm:grid-cols-2">
      {SUGGESTIONS.map(({ Icon, title, body, prompt }) => (
        <Card
          key={title}
          interactive
          role="button"
          tabIndex={0}
          onClick={() => requestComposerDraft(prompt)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              requestComposerDraft(prompt);
            }
          }}
          className="group p-3 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-accent)]"
        >
          <div className="flex items-center gap-2">
            <Icon className="h-4 w-4 shrink-0 text-[var(--color-accent)]" aria-hidden />
            <span className="truncate text-xs font-medium text-[var(--color-foreground)]">{title}</span>
          </div>
          <p className="mt-1.5 text-2xs leading-relaxed text-[var(--color-foreground-muted)]">{body}</p>
        </Card>
      ))}
    </div>
  );
}
