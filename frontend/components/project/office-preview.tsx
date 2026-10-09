"use client";
import { useEffect, useRef, useState } from "react";
import { useT } from "@/lib/i18n";
import {
  acquirePreviewParse,
  PREVIEW_LIMITS,
  PreviewFailure,
  reservePreview,
} from "@/lib/preview-budget";
import {
  prepareOfficeFrame,
  type OfficeFrame,
  type OfficeKind,
} from "@/lib/office-frame";
import type { PreviewRepresentation } from "@/lib/preview-representation";

import { PreviewStatus } from "./preview-status";

type Phase = "queued" | "parsing" | "ready" | "failed" | "canceled";

export function OfficePreview({
  representation,
  kind,
  path,
  href,
}: {
  representation: PreviewRepresentation;
  kind: OfficeKind;
  path: string;
  href: string;
}) {
  const tr = useT();
  const host = useRef<HTMLDivElement>(null);
  const active = useRef<OfficeFrame | null>(null);
  const frames = useRef(new Set<OfficeFrame>());
  const cancel = useRef(() => {});
  const [hasReady, setHasReady] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<{ phase: Phase; drawings: boolean; failure?: PreviewFailure }>({
    phase: "queued",
    drawings: false,
  });
  useEffect(() => {
    const owned = frames.current;
    return () => {
      for (const frame of owned) frame.dispose();
      owned.clear();
      active.current = null;
    };
  }, []);
  useEffect(() => {
    const element = host.current;
    if (!element) return;
    const abort = new AbortController();
    // Adoption transfers cancellation ownership to the displayed frame. A new
    // candidate cancels only its predecessor's pending parse, not displayed data.
    const frameAbort = new AbortController();
    let candidate: OfficeFrame | undefined,
      adopted = false;
    cancel.current = () => {
      if (adopted) return;
      abort.abort();
      frameAbort.abort();
      setState((old) => ({ ...old, phase: "canceled" }));
    };
    const releaseRepresentation = representation.retain();
    let releaseRenderer = () => {};
    let released = false;
    const release = () => {
      if (released) return;
      released = true;
      releaseRenderer();
      releaseRepresentation();
    };
    void (async () => {
      let unlock = () => {};
      try {
        await Promise.resolve();
        abort.signal.throwIfAborted();
        setState((old) => ({ ...old, phase: "queued" }));
        unlock = await acquirePreviewParse(abort.signal);
        abort.signal.throwIfAborted();
        releaseRenderer = reservePreview(
          representation.blob.size +
            (representation.plan?.renderBytes ?? PREVIEW_LIMITS.renderBytes),
          "renderers",
        );
        setState((old) => ({ ...old, phase: "parsing" }));
        const frame = prepareOfficeFrame(
          element,
          representation,
          kind,
          frameAbort.signal,
        );
        candidate = {
          ready: frame.ready,
          position: frame.position,
          activate: (previous) => frame.activate(previous),
          dispose() {
            frame.dispose();
            release();
            frames.current.delete(candidate!);
          },
        };
        frames.current.add(candidate);
        const result = await frame.ready;
        abort.signal.throwIfAborted();
        const previous = active.current;
        candidate.activate(previous ?? undefined);
        active.current = candidate;
        adopted = true;
        previous?.dispose();
        setHasReady(true);
        setState({ phase: "ready", drawings: result.drawingsUnavailable });
      } catch (error) {
        if (!abort.signal.aborted)
          setState((old) => ({ ...old, phase: "failed", failure: error instanceof PreviewFailure ? error : new PreviewFailure("renderer_failed", "render", { retryable: true }) }));
      } finally {
        if (!adopted) {
          candidate?.dispose();
          release();
        }
        unlock();
      }
    })();
    return () => {
      abort.abort();
      if (!adopted) frameAbort.abort();
    };
  }, [representation, kind, path, attempt]);
  return (
    <>
      {state.drawings && (
        <p className="notice">
          {tr(
            "Some drawings are unavailable in this preview. Download the workbook to view them.",
          )}
        </p>
      )}
      {(state.phase === "failed" || state.phase === "canceled") && (
        <PreviewStatus path={path} href={href} size={representation.blob.size}
          title={tr(state.phase === "canceled" ? "Preview canceled." : "Preview unavailable")}
          description={state.phase === "canceled" ? tr("You can try again or download the original file.")
            : state.failure?.code === "resource_busy" ? tr("Preview resources are busy. Try again in a moment.")
            : state.failure?.code === "parse_timeout" ? tr("Preparing this preview took too long. Retry or download the file.")
            : tr("The preview renderer stopped. Retry or download the file.")}
          onRetry={() => setAttempt(value => value + 1)} />
      )}
      {(state.phase === "queued" || state.phase === "parsing") && (
        <PreviewStatus path={path} href={href} size={representation.blob.size} loading
          title={tr("Preparing preview…")}
          description={tr("Larger files may take a moment. You can keep working while the preview loads.")}
          onCancel={() => cancel.current()} />
      )}
      <div
        className="office-preview"
        style={!hasReady ? { flex: "0 0 0" } : undefined}
        ref={host}
        data-preview-phase={state.phase}
        aria-busy={state.phase === "queued" || state.phase === "parsing"}
      />
    </>
  );
}
