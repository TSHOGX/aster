"use client";

import { BatteryCharging, Gauge, Thermometer, TriangleAlert } from "lucide-react";

import { Card } from "@/components/ui/card";
import { requestComposerDraft } from "@/lib/composer-draft";
import { useT, type MessageKey } from "@/lib/i18n";

/**
 * Starting points offered on an empty session.
 *
 * Deliberately domain-specific: a generic "ask me anything" tells a new user
 * nothing about what this workbench is for, whereas these name the questions the
 * agent actually answers — fleet charging behaviour and BMS faults out of the
 * warehouse, plus one file-based log review. Picking one fills the composer rather
 * than sending, since the period, platform or file still wants adjusting.
 */
/*
  Only the icon and the key prefix live here; the three strings per card come from
  the message catalog.

  The `prompt` — the text dropped into the composer — is translated too, not just
  the card's title and body. These prompts are starting points meant to be edited
  before sending, so filling a Chinese user's composer with English defeats the
  purpose: they would have to read English to change the month or platform. Domain
  notation stays as-is in both locales (SOC, BMS, F2, C-rate, /workspace).
*/
/*
  Keys are spelled out rather than built as `prompt.${key}.title`: a template
  literal needs an `as MessageKey` cast to typecheck, and that cast is exactly
  what would stop the compiler from catching a renamed or missing catalog entry.
*/
const SUGGESTIONS = [
  {
    Icon: BatteryCharging,
    titleKey: "prompt.acdc.title",
    bodyKey: "prompt.acdc.body",
    promptKey: "prompt.acdc.prompt"
  },
  {
    Icon: Gauge,
    titleKey: "prompt.startsoc.title",
    bodyKey: "prompt.startsoc.body",
    promptKey: "prompt.startsoc.prompt"
  },
  {
    Icon: Thermometer,
    titleKey: "prompt.thermal.title",
    bodyKey: "prompt.thermal.body",
    promptKey: "prompt.thermal.prompt"
  },
  {
    Icon: TriangleAlert,
    titleKey: "prompt.faults.title",
    bodyKey: "prompt.faults.body",
    promptKey: "prompt.faults.prompt"
  }
] as const satisfies readonly {
  Icon: typeof BatteryCharging;
  titleKey: MessageKey;
  bodyKey: MessageKey;
  promptKey: MessageKey;
}[];

export function PromptSuggestions() {
  const t = useT();
  return (
    <div className="grid w-full gap-2.5 sm:grid-cols-2">
      {SUGGESTIONS.map(({ Icon, titleKey, bodyKey, promptKey }) => {
        const title = t(titleKey);
        const body = t(bodyKey);
        const prompt = t(promptKey);
        return (
        <Card
          key={titleKey}
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
        );
      })}
    </div>
  );
}
