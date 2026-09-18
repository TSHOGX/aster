"use client";

import { useEffect, useRef } from "react";
import hljs from "highlight.js";

// highlight.js runs synchronously on the whole file, so on large files it blocks
// the main thread long enough to freeze the tab. Above this size we skip
// highlighting entirely and let the plain-text children render instead.
const MAX_HIGHLIGHT_BYTES = 8192;

export function CodeView({ code, language }: { code: string; language?: string }) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!ref.current) return;
    // Bail before any hljs work on large files. We return without touching
    // innerHTML, so the {code} text child React rendered stays visible as
    // plain text — no highlight, but the tab stays responsive.
    if (new TextEncoder().encode(code).length > MAX_HIGHLIGHT_BYTES || !language) return;
    if (language) {
      try {
        const result = hljs.highlight(code, { language, ignoreIllegals: true });
        ref.current.innerHTML = result.value;
        return;
      } catch {
        // fall through
      }
    }
    // Unknown or unlabeled languages stay as escaped plain text.
  }, [code, language]);
  return (
    <pre className="h-full overflow-auto bg-[var(--color-code-bg)] px-6 py-4 font-mono text-xs leading-relaxed text-[var(--color-code-ink)]">
      <code ref={ref} className={language ? `language-${language}` : undefined}>{code}</code>
    </pre>
  );
}
