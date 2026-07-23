"use client";

import { memo } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";

import { cn } from "@/lib/cn";

// Parsing markdown + syntax highlighting is expensive, so we memoize: with a
// shallow prop compare a committed bubble never re-parses, and during a stream
// this only re-renders when `content` actually changes.
function MarkdownViewImpl({ content, className }: { content: string; className?: string }) {
  return (
    <div className={cn("markdown-body", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]}>
        {content}
      </ReactMarkdown>
    </div>
  );
}

export const MarkdownView = memo(MarkdownViewImpl);
MarkdownView.displayName = "MarkdownView";
