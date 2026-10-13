"use client";
import { useMemo, type ComponentProps } from "react";
import { TurnView } from "./turn-view";
import type { ConversationTurn } from "@/lib/conversation-turns";
import type { FileSource } from "@/lib/file-source";
import { workspacePath } from "@/lib/workspace-links";
import { WorkspaceImageSource } from "@/components/workbench/markdown-view";

// Shared by the interactive Session and both read-only surfaces. Execution
// controls live in their owning container, never in this presentation layer.
export function ConversationReader({
  turns,
  fileSource,
  ...props
}: Omit<ComponentProps<typeof TurnView>, "turn"> & {
  turns: ConversationTurn[];
  fileSource?: FileSource;
}) {
  const resolveImage = useMemo(
    () =>
      fileSource
        ? (url: string) => {
            let path: string | null = null;
            try {
              path = workspacePath(decodeURIComponent(url), props.files);
            } catch {
              return "";
            }
            if (path) return fileSource.content(path) || "";
            if (url.startsWith("/") || !/^https?:\/\//.test(url)) return "";
            return url;
          }
        : undefined,
    [fileSource, props.files],
  );
  return (
    <WorkspaceImageSource value={resolveImage}>
      {turns.map((turn) => (
        <TurnView key={turn.id} turn={turn} {...props} />
      ))}
    </WorkspaceImageSource>
  );
}
