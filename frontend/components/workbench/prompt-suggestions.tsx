"use client";

import { Activity, FlaskConical, LineChart, Thermometer } from "lucide-react";

import { Card } from "@/components/ui/card";
import { requestComposerDraft } from "@/lib/composer-draft";
import { useT, type MessageKey } from "@/lib/i18n";

/**
 * Starting points offered on an empty session.
 *
 * Deliberately domain-specific: a generic "ask me anything" tells a new user
 * nothing about what this workbench is for, whereas these name the artifacts the
 * agent actually works with (cycling data, dQ/dV curves, thermal logs). Picking
 * one fills the composer rather than sending, since every prompt needs the user's
 * own file or cell name substituted in.
 */
/*
  Only the icon and the key prefix live here; the three strings per card come from
  the message catalog.

  The `prompt` — the text dropped into the composer — is translated too, not just
  the card's title and body. These prompts are starting points meant to be edited
  before sending, so filling a Chinese user's composer with English defeats the
  purpose: they would have to read English to change the file name. Domain
  notation stays as-is in both locales (dQ/dV, SOH, C-rate, /workspace).
*/
/*
  Keys are spelled out rather than built as `prompt.${key}.title`: a template
  literal needs an `as MessageKey` cast to typecheck, and that cast is exactly
  what would stop the compiler from catching a renamed or missing catalog entry.
*/
const SUGGESTIONS = [
  {
    Icon: LineChart,
    titleKey: "prompt.fade.title",
    bodyKey: "prompt.fade.body",
    promptKey: "prompt.fade.prompt"
  },
  {
    Icon: Activity,
    titleKey: "prompt.dqdv.title",
    bodyKey: "prompt.dqdv.body",
    promptKey: "prompt.dqdv.prompt"
  },
  {
    Icon: Thermometer,
    titleKey: "prompt.thermal.title",
    bodyKey: "prompt.thermal.body",
    promptKey: "prompt.thermal.prompt"
  },
  {
    Icon: FlaskConical,
    titleKey: "prompt.compare.title",
    bodyKey: "prompt.compare.body",
    promptKey: "prompt.compare.prompt"
  }
] as const satisfies readonly {
  Icon: typeof LineChart;
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
