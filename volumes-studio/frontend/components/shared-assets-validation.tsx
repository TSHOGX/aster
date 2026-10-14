"use client";

import { useEffect, useState } from "react";
import { formatQuantity } from "../../../web/i18n/locale";
import { api, bytes } from "../lib/api";
import { useT } from "../lib/i18n";

type Validation = {
  valid: boolean;
  revision: number;
  findings: { path: string; severity: string; message: string }[];
  skill_count: number;
  agents_bytes: string;
  agents_preview: string;
  preview_truncated: boolean;
};

export function SharedAssetsValidation({ library, version, onOpen }: {
  library: string;
  version: string;
  onOpen?: () => void;
}) {
  const tr = useT();
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<{
    source: string;
    result?: Validation;
    error?: string;
  }>();
  const source = `/libraries/${encodeURIComponent(library)}/${version ? `versions/${encodeURIComponent(version)}` : "draft"}/validation`;
  const result = state?.source === source ? state.result : undefined;
  const error = state?.source === source ? state.error : undefined;

  useEffect(() => {
    const abort = new AbortController();
    void api<Validation>(source, { signal: abort.signal }).then(
      result => { if (!abort.signal.aborted) setState({ source, result }); },
      error => { if (!abort.signal.aborted) setState({ source, error: String(error) }); },
    );
    return () => abort.abort();
  }, [source, attempt]);

  return <details className="notice shared-assets-validation" open={error || result && !result.valid ? true : undefined}>
    <summary>{tr("Shared assets")} · {tr(error ? "Validation unavailable" : !result ? "Checking saved content…" : result.valid ? version ? "Validated published version" : "Ready to publish" : "Needs attention")}</summary>
    <p>{tr("AGENTS.md is appended to the Agent system prompt. Skills are optional. Publishing creates an immutable version; existing Projects retain their current version until a reviewed rollout.")}</p>
    {error && <div role="alert">
      <p>{tr(error)}</p>
      <button type="button" onClick={() => { setState(undefined); setAttempt(value => value + 1); }}>{tr("Retry validation")}</button>
    </div>}
    {result && <>
      <p>{version ? tr("Published version · Read-only") : tr("Saved draft revision {revision}", { revision: formatQuantity(result.revision) })} · {tr("{count} skills", { count: formatQuantity(result.skill_count) })} · {tr("AGENTS.md: {size}", { size: result.agents_bytes && result.agents_bytes !== "0" ? bytes(result.agents_bytes) : tr("Missing") })}</p>
      {result.findings.length > 0 && <ul>{result.findings.map((finding, i) => <li key={i}>
        <strong>{tr(finding.severity === "error" ? "Fix" : "Note")}: <code>{finding.path}</code></strong> — {tr(finding.message)}
      </li>)}</ul>}
      {onOpen && result.agents_bytes && result.agents_bytes !== "0" && <button type="button" onClick={onOpen}>{tr("Open AGENTS.md in editor")}</button>}
      <details>
        <summary>{tr("AGENTS.md text preview")}{result.preview_truncated ? tr(" (first 16 KiB)") : ""}</summary>
        <pre>{result.agents_preview || tr("Empty")}</pre>
      </details>
    </>}
  </details>;
}
