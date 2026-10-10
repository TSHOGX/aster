"use client";
import { useMemo, useState } from "react";
import { classifyArtifact } from "@/lib/mime";
import { parseDelimited } from "@/lib/file-view";
import { isNumericText } from "@/lib/table-cells";
import { useT } from "@/lib/i18n";
import { CodeView } from "@/components/workbench/viewers/code-view";
import { HTMLPreview } from "./html-preview";

export function TextPreview({path, text, href}: {path: string; text: string; href?: string}) {
  const kind = classifyArtifact(path);
  if (kind.kind === "html") return <HTMLPreview text={text} path={path} href={href} />;
  if (kind.kind === "table") return <DelimitedPreview path={path} text={text} delimiter={kind.delimiter ?? ","} />;
  let formatted = text;
  try { formatted = JSON.stringify(JSON.parse(text), null, 2); } catch { /* Preserve readable source for incomplete JSON. */ }
  return <CodeView code={formatted} language="json" />;
}

function DelimitedPreview({path, text, delimiter}: {path: string; text: string; delimiter: "," | "\t"}) {
  const tr = useT();
  const [limit, setLimit] = useState(100);
  const parsed = useMemo(() => {
    try { return parseDelimited(text, delimiter); } catch { return null; }
  }, [text, delimiter]);
  if (!parsed) return <p className="notice">{tr("This table could not be formatted. Choose Source to view the original text.")}</p>;
  if (!parsed.rows.length) return <p className="notice">{tr("This file is empty.")}</p>;
  const columns = Math.max(...parsed.rows.map(row => row.length));
  return <div className="delimited-preview">
    <div className="table-summary">
      <span>{tr("Table")}</span>
      <span>{tr("{rows} rows · {columns} columns", {rows:parsed.rows.length, columns})}</span>
    </div>
    {parsed.truncated && <p className="list-hint" role="status">{tr("This table is larger than the in-app preview. Source and Download contain the complete file.")}</p>}
    <div className="delimited-scroll" tabIndex={0} role="region" aria-label={`${tr("Table")} ${path}`} onScroll={event => {
      const node = event.currentTarget;
      if (node.scrollTop + node.clientHeight > node.scrollHeight - 240)
        setLimit(value => Math.min(parsed.rows.length, value + 100));
    }}>
      <table><caption className="sr-only">{path}</caption>
        <thead><tr><th aria-label={tr("Row")} />{Array.from({length:columns}, (_, i) => <th key={i} scope="col">{i + 1}</th>)}</tr></thead>
        <tbody>{parsed.rows.slice(0, limit).map((row, i) => <tr key={i}>
          <th scope="row">{i + 1}</th>
          {Array.from({length:columns}, (_, j) => <td key={j} data-numeric={isNumericText(row[j] ?? "")} title={row[j]}>{row[j] ?? ""}</td>)}
        </tr>)}</tbody>
      </table>
      {limit < parsed.rows.length && <p className="table-load-status" role="status">{tr("Scroll to load more rows")}</p>}
    </div>
  </div>;
}
