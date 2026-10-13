"use client";
import { useState } from "react";
import { Maximize2, Minimize2, X } from "lucide-react";
import { sharedFileSource } from "@/lib/file-source";
import type { ConversationSource } from "@/lib/conversation-source";
import { hasSourceView } from "@/lib/file-view";
import { useT } from "@/lib/i18n";
import { FilePreview } from "./file-preview";
import { SharedFilesBrowser } from "./shared-files-browser";
import { IconButton } from "./ui";

export function SharedFilesPanel({
  source,
  paths,
  selected,
  validation,
  onSelect,
  fullscreen,
  onFullscreen,
  onClose,
}: {
  source: ConversationSource;
  paths: string[];
  selected: string;
  validation: number;
  onSelect: (path: string) => void;
  fullscreen: boolean;
  onFullscreen: () => void;
  onClose: () => void;
}) {
  const files = sharedFileSource(source, paths);
  const tr = useT();
  const [mode, setMode] = useState<"preview" | "source">("preview");
  return (
    <aside className="files-panel tree-open" aria-label={tr("Shared files")}>
      <header className="file-toolbar">
        <nav className="file-breadcrumb" aria-label={tr("File path")}>
          <span>{tr("Shared files")}</span>
          {selected && <strong>{selected}</strong>}
        </nav>
        <div className="row-actions">
          {selected && hasSourceView(selected) && (
            <div
              className="file-view-toggle"
              role="group"
              aria-label={tr("File view")}
            >
              <button
                aria-pressed={mode === "preview"}
                onClick={() => setMode("preview")}
              >
                {tr("Preview")}
              </button>
              <button
                aria-pressed={mode === "source"}
                onClick={() => setMode("source")}
              >
                {tr("Source")}
              </button>
            </div>
          )}
          {selected && source.capabilities.download && (
            <a href={files.download(selected)} download>
              {tr("Download")}
            </a>
          )}
          <IconButton
            label={tr(fullscreen ? "Exit Full Screen" : "Enter Full Screen")}
            onClick={onFullscreen}
          >
            {fullscreen ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
          </IconButton>
          <IconButton label={tr("Close file")} onClick={onClose}>
            <X size={16} />
          </IconButton>
        </div>
      </header>
      <div className="file-columns">
        <div className="selected-file">
          {selected ? (
            <FilePreview
              key={selected}
              validation={validation}
              project={source.key}
              path={selected}
              mode={mode}
              href={files.content(selected)!}
              downloadHref={
                source.capabilities.download
                  ? files.download(selected)
                  : undefined
              }
            />
          ) : (
            <div className="empty-state">
              {tr(paths.length ? "Choose files" : "No files have been shared.")}
            </div>
          )}
        </div>
        <section className="file-tree" aria-label={tr("File tree")}>
          <SharedFilesBrowser
            paths={paths}
            selected={selected}
            onSelect={(path) => {
              setMode("preview");
              onSelect(path);
            }}
          />
        </section>
      </div>
    </aside>
  );
}
