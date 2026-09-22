"use client";

import { memo, useState, type ComponentPropsWithoutRef } from "react";
import type { PluggableList } from "unified";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import { Check, Copy } from "lucide-react";

import { HIGHLIGHT_LANGUAGES } from "@/lib/highlight-languages";

import { workspaceLinkPlugin } from "@/lib/workspace-links";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n";
import { isNumericText } from "@/lib/table-cells";
import { richMarkdownAllowed } from "@/lib/markdown-budget";

/*
  Minimal structural view of the hast nodes react-markdown hands to component
  overrides. We only ever read text content and the code element's class list, so
  a narrow local type is preferable to pulling in the full hast typings.
*/
type HastNode = {
  type?: string;
  tagName?: string;
  value?: string;
  properties?: { className?: unknown };
  children?: HastNode[];
};

/**
 * Flattens a node's text.
 *
 * Needed because rehype-highlight replaces a code block's plain-text child with
 * a tree of <span> tokens, so `children` is no longer readable as a string —
 * neither for copying code nor for deciding whether a table cell is numeric.
 */
function nodeText(node: HastNode | undefined): string {
  if (!node) return "";
  if (typeof node.value === "string") return node.value;
  return (node.children ?? []).map(nodeText).join("");
}

/** True for table cells holding nothing but a number (see lib/table-cells.ts). */
function isNumericCell(node: HastNode | undefined): boolean {
  return isNumericText(nodeText(node));
}

const LANGUAGE_CLASS = /^language-(.+)$/;

/** Reads `language-xxx` off the code element inside a <pre>. */
function codeLanguage(pre: HastNode | undefined): string | null {
  const code = (pre?.children ?? []).find((child) => child.tagName === "code");
  const classes = code?.properties?.className;
  const list = Array.isArray(classes)
    ? classes
    : typeof classes === "string"
      ? [classes]
      : [];
  for (const entry of list) {
    if (typeof entry !== "string") continue;
    const match = LANGUAGE_CLASS.exec(entry);
    if (match) return match[1];
  }
  return null;
}

/**
 * A fenced code block with a header strip: language on the left, copy on the
 * right. The agent's replies are mostly code and tables, so having to hand-select
 * a block to copy it was the single most repeated friction in the view.
 */
function CodeBlock({
  node,
  children,
  ...rest
}: ComponentPropsWithoutRef<"pre"> & { node?: HastNode }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const language = codeLanguage(node);
  const source = nodeText(node);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(source);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // Clipboard access can be denied (insecure context, permission policy).
      // Selecting the text by hand still works, so this needs no error surface.
    }
  };

  return (
    <div className="code-block">
      <div className="code-block-header">
        <span className="code-block-lang">{language ?? "text"}</span>
        <button
          type="button"
          onClick={() => void copy()}
          className="code-block-copy"
          aria-label={copied ? t("code.copied") : t("code.copyCode")}
          title={copied ? t("code.copied") : t("code.copyCode")}
        >
          {copied ? (
            <Check className="h-3 w-3" aria-hidden />
          ) : (
            <Copy className="h-3 w-3" aria-hidden />
          )}
          {copied ? t("code.copied") : t("code.copy")}
        </button>
      </div>
      <pre {...rest}>{children}</pre>
    </div>
  );
}

const COMPONENTS: Components = {
  pre: CodeBlock,
  // A wide table has to scroll inside the message rather than stretch it, and the
  // sticky header needs a bounded scroll container to stick to.
  table: ({ node: _node, children, ...rest }) => (
    <div className="table-scroll">
      <table {...rest}>{children}</table>
    </div>
  ),
  td: ({ node, children, ...rest }) => (
    <td
      data-numeric={isNumericCell(node as HastNode) ? "true" : undefined}
      {...rest}
    >
      {children}
    </td>
  ),
  // Header cells mirror their column's alignment, so a numeric column's label
  // sits over the digits instead of drifting to the far side.
  th: ({ node, children, ...rest }) => (
    <th
      data-numeric={isNumericCell(node as HastNode) ? "true" : undefined}
      {...rest}
    >
      {children}
    </th>
  ),
};

const REHYPE_PLUGINS: PluggableList = [
  () => (tree: HastNode) => {
    const walk = (node: HastNode) => {
      if (
        node.tagName === "code" &&
        new TextEncoder().encode(nodeText(node)).length > 8192
      )
        node.properties = { ...node.properties, className: ["no-highlight"] };
      node.children?.forEach(walk);
    };
    walk(tree);
  },
  [
    rehypeHighlight,
    {
      detect: false,
      // Includes lowlight's `common` set — this option replaces the plugin's
      // registry rather than extending it. See lib/highlight-languages.ts.
      languages: HIGHLIGHT_LANGUAGES,
    },
  ],
];

// Parsing markdown + syntax highlighting is expensive, so we memoize: with a
// shallow prop compare a committed bubble never re-parses, and during a stream
// this only re-renders when `content` actually changes.
function MarkdownViewImpl({
  content,
  className,
  highlight = true,
  workspaceFiles,
}: {
  content: string;
  className?: string;
  highlight?: boolean;
  workspaceFiles?: readonly string[];
}) {
  const tr = useT();
  if (!richMarkdownAllowed(content))
    return (
      <div
        className={cn("markdown-body", className)}
        data-markdown-mode="plain"
      >
        <p className="notice" role="status">
          {tr(
            "This Markdown is shown as plain text to keep the preview responsive.",
          )}
        </p>
        <pre className="markdown-plain">{content}</pre>
      </div>
    );
  return (
    <div className={cn("markdown-body", className)} data-markdown-mode="rich">
      <ReactMarkdown
        remarkPlugins={
          workspaceFiles
            ? [remarkGfm, [workspaceLinkPlugin, { known: workspaceFiles }]]
            : [remarkGfm]
        }
        rehypePlugins={highlight ? REHYPE_PLUGINS : []}
        components={COMPONENTS}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}

export const MarkdownView = memo(MarkdownViewImpl);
MarkdownView.displayName = "MarkdownView";
