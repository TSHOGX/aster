"use client";
import { useEffect, useMemo, useState } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { classifyArtifact } from "@/lib/mime";
import { parseDelimited } from "@/lib/file-view";
import { isNumericText } from "@/lib/table-cells";
import { useT } from "@/lib/i18n";
import { reservePreview } from "@/lib/preview-budget";
import { CodeView } from "@/components/workbench/viewers/code-view";

// An opaque-origin frame can run self-contained charts, but cannot access the
// application, credentials, network services, popups, forms or top navigation.
const HTML_POLICY = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";
function HTMLPreview({text, path}: {text: string; path: string}) {
  const tr = useT();
  const [admitted, setAdmitted] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let release = () => {};
    const ready = setTimeout(() => {
      try { release = reservePreview(0, "frames"); setAdmitted(true); }
      catch { setFailed(true); }
    }, 0);
    return () => { clearTimeout(ready); release(); };
  }, []);
  if (failed) return <p className="notice">{tr("Preview is busy. Choose Source to read this file.")}</p>;
  if (!admitted) return <p className="notice">{tr("Loading preview…")}</p>;
  return <iframe className="document-frame html-preview" title={`${tr("Preview")} ${path}`} sandbox="allow-scripts" referrerPolicy="no-referrer"
    srcDoc={`<!doctype html><meta http-equiv="Content-Security-Policy" content="${HTML_POLICY}"><meta name="referrer" content="no-referrer">${text}`} />;
}

export function TextPreview({path, text}: {path: string; text: string}) {
  const kind = classifyArtifact(path);
  if (kind.kind === "html") return <HTMLPreview text={text} path={path} />;
  if (kind.kind === "table") return <DelimitedPreview path={path} text={text} delimiter={kind.delimiter ?? ","} />;
  let formatted = text;
  try { formatted = JSON.stringify(JSON.parse(text), null, 2); } catch { /* Preserve readable source for incomplete JSON. */ }
  return <CodeView code={formatted} language="json" />;
}

function DelimitedPreview({path, text, delimiter}: {path: string; text: string; delimiter: "," | "\t"}) {
  const tr = useT();
  const [page, setPage] = useState(0);
  const parsed = useMemo(() => {
    try { return parseDelimited(text, delimiter); } catch { return null; }
  }, [text, delimiter]);
  if (!parsed) return <p className="notice">{tr("This table could not be formatted. Choose Source to view the original text.")}</p>;
  if (!parsed.rows.length) return <p className="notice">{tr("This file is empty.")}</p>;
  const columns = Math.max(...parsed.rows.map(row => row.length));
  const pages = Math.ceil(parsed.rows.length / 100);
  return <div className="delimited-preview">
    <div className="table-summary">
      <span>{tr("{rows} rows · {columns} columns", {rows:parsed.rows.length, columns})}</span>
      {pages > 1 && <div className="table-pagination">
        <button aria-label={tr("Previous rows")} disabled={page === 0} onClick={() => setPage(n => n - 1)}><ChevronLeft size={15} /></button>
        <span>{page + 1} / {pages}</span>
        <button aria-label={tr("Next rows")} disabled={page + 1 === pages} onClick={() => setPage(n => n + 1)}><ChevronRight size={15} /></button>
      </div>}
    </div>
    {parsed.truncated && <p className="list-hint" role="status">{tr("Showing the first part of this table. Source and Download contain the complete file.")}</p>}
    <div className="delimited-scroll" tabIndex={0} role="region" aria-label={`${tr("Table")} ${path}`}>
      <table><caption className="sr-only">{path}</caption>
        <thead><tr><th aria-label={tr("Row")} />{Array.from({length:columns}, (_, i) => <th key={i} scope="col">{i + 1}</th>)}</tr></thead>
        <tbody>{parsed.rows.slice(page * 100, (page + 1) * 100).map((row, i) => <tr key={i}>
          <th scope="row">{page * 100 + i + 1}</th>
          {Array.from({length:columns}, (_, j) => <td key={j} data-numeric={isNumericText(row[j] ?? "")} title={row[j]}>{row[j] ?? ""}</td>)}
        </tr>)}</tbody>
      </table>
    </div>
  </div>;
}
