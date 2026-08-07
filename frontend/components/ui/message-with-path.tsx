"use client";

import { splitAround, type MessageKey, useT } from "@/lib/i18n";
import { cn } from "@/lib/cn";

/**
 * Renders a catalog string whose `{path}` placeholder should appear as a mono
 * code chip rather than plain text.
 *
 * The sentence's word order stays in the catalog (Chinese puts the path in a
 * different position than English does, and appends 目录 after it), while the
 * chip's styling stays here.
 */
export function MessageWithPath({
  messageKey,
  path,
  className,
  codeClassName
}: {
  messageKey: MessageKey;
  path: string;
  className?: string;
  codeClassName?: string;
}) {
  const t = useT();
  // Called without params, t() returns the template with `{path}` still in it —
  // which is what we need to split on.
  const template = t(messageKey);
  const parts = splitAround(template, "path");
  const chip = (
    <code className={cn("font-mono", codeClassName)}>{path}</code>
  );
  return (
    <span className={className}>
      {parts === null ? (
        // No placeholder in this locale's string — render it as written rather
        // than dropping the path silently.
        template
      ) : (
        <>
          {parts.before}
          {chip}
          {parts.after}
        </>
      )}
    </span>
  );
}
