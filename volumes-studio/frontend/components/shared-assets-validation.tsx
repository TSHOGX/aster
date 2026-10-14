"use client";
import { useEffect, useState } from "react";
import { api, bytes } from "../lib/api";

type Validation = { valid: boolean; revision: number; findings: {path:string;severity:string;message:string}[]; skill_count:number; agents_bytes:string; agents_preview:string; preview_truncated:boolean };
export function SharedAssetsValidation({library,version,onOpen}:{library:string;version:string;onOpen?:()=>void}) {
 const [result,setResult]=useState<Validation|null>(null),[error,setError]=useState("");
 useEffect(()=>{
  const abort=new AbortController();
  void api<Validation>(`/libraries/${encodeURIComponent(library)}/${version ? `versions/${encodeURIComponent(version)}` : "draft"}/validation`,{signal:abort.signal}).then(setResult).catch(e=>{if(!abort.signal.aborted)setError(String(e));});
  return ()=>abort.abort();
 },[library,version]);
 return <details className="notice" open={result ? !result.valid : false}>
  <summary>Shared assets · {error ? "Validation unavailable" : !result ? "Checking saved content…" : result.valid ? version ? "Validated published version" : "Ready to publish" : "Needs attention"}</summary>
  <p>AGENTS.md is appended to the Agent system prompt. Skills are optional. Publishing creates an immutable version; existing Projects retain their current version until a reviewed rollout.</p>
  {error && <p role="alert">{error}</p>}
  {result && <>
   <p>{version ? "Published version · Read-only" : `Saved draft revision ${result.revision}`} · {result.skill_count} skills · AGENTS.md: {result.agents_bytes ? bytes(result.agents_bytes) : "Missing"}</p>
   <ul>{result.findings.map((f,i)=><li key={i}><strong>{f.severity === "error" ? "Fix" : "Note"}: {f.path}</strong> — {f.message}</li>)}</ul>
   {onOpen && result.agents_bytes && <button type="button" onClick={onOpen}>Open AGENTS.md in editor</button>}
   <details><summary>AGENTS.md text preview{result.preview_truncated ? " (first 16 KiB)" : ""}</summary><pre style={{whiteSpace:"pre-wrap",maxHeight:240,overflow:"auto"}}>{result.agents_preview || "Empty"}</pre></details>
  </>}
 </details>;
}
