"use client";

import {
  Braces,
  Code2,
  File,
  FileText,
  FileType,
  Image as ImageIcon,
  Presentation,
  Sheet,
  Table2
} from "lucide-react";

import { classifyArtifact } from "@/lib/mime";
import { cn } from "@/lib/cn";

/**
 * The file-kind glyph, shared by the workspace tree and the mentioned-files
 * section so one path never draws as two different icons in the same panel.
 */
export function ArtifactIcon({ path, className }: { path: string; className?: string }) {
  const kind = classifyArtifact(path).kind;
  const merged = cn("h-3.5 w-3.5 shrink-0 text-[var(--color-foreground-muted)]", className);
  switch (kind) {
    case "image":
      return <ImageIcon className={merged} />;
    case "code":
      return <Code2 className={merged} />;
    case "json":
      return <Braces className={merged} />;
    case "table":
      return <Table2 className={merged} />;
    case "spreadsheet":
      return <Sheet className={merged} />;
    case "document":
      return <FileType className={merged} />;
    case "presentation":
      return <Presentation className={merged} />;
    case "markdown":
    case "text":
      return <FileText className={merged} />;
    default:
      return <File className={merged} />;
  }
}
