import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

// Scoped bug fixes to pptx-preview 1.0.7 (ISC). Refuse a changed upstream source
// until its event ownership has been reviewed; never guess at a minified match.
const upstreamSHA256 =
  "03090ed5000d89b5756b1ae9f3466d6f8ab855ffe521f49b22b2d2f1ee7b2f59";
const changes = [
  [
    'function g(t,e,a){var r={type:"solidFill"};',
    'function g(t,e,a){if(!t)return{type:"none"};var r={type:"solidFill"};',
  ],
  [
    '.join("")||"图表标题"},o.prototype.parseBarColors',
    '.join("")},o.prototype.parseBarColors',
  ],
  [
    "function bt(t,e){yt?yt[t]&&(yt[t]=yt[t].filter((function(t){return t!==e}))):yt[t]=[]}",
    "function bt(t,e){if(yt[t]){if(e)yt[t]=yt[t].filter((function(t){return t!==e}));else delete yt[t]}}",
  ],
  [
    't&&this.wrapper.removeChild(t),wt("removeSlide")},t.prototype.renderNextSlide',
    't&&this.wrapper.removeChild(t),wt("removeSlide"),bt("removeSlide"),bt("destroy")},t.prototype.renderNextSlide',
  ],
  [
    'prototype.destroy=function(){wt("destroy"),bt("destroy")}',
    'prototype.destroy=function(){wt("destroy"),bt("destroy"),bt("removeSlide")}',
  ],
  [
    'setTimeout((function(){var t=h.init(e,null,{renderer:"svg"});',
    'setTimeout((function(){if(!e.isConnected)return;var t=h.init(e,null,{renderer:"svg"});',
  ],
  [
    't.setOption(i),vt("destroy",',
    't.setOption(Object.assign({},i,{animation:false})),vt("destroy",',
  ],
  [
    'prototype.parseBarColors=function(t){var e=this;return t.map((function(t){return S(g(c(t,["c:spPr","a:solidFill"]),e.theme,e.ctx))}))}',
    'prototype.parseBarColors=function(t){var e=this;return t.map((function(t,a){return S(g(c(t,["c:spPr","a:solidFill"]),e.theme,e.ctx))||e.theme.getColor("accent"+(a%6+1))}))}',
  ],
  [
    'prototype.parseLineColors=function(t){var e=this;return t.map((function(t){return S(g(c(t,["c:spPr","a:ln","a:solidFill"])||c(t,["c:spPr","a:solidFill"]),e.theme,e.ctx))}))}',
    'prototype.parseLineColors=function(t){var e=this;return t.map((function(t,a){return S(g(c(t,["c:spPr","a:ln","a:solidFill"])||c(t,["c:spPr","a:solidFill"]),e.theme,e.ctx))||e.theme.getColor("accent"+(a%6+1))}))}',
  ],
];
const hash = (source) => createHash("sha256").update(source).digest("hex");
export function patchPresentationSource(source) {
  // Reconstruct and verify the exact original when an install was already patched.
  let original = source;
  for (const [before, after] of changes)
    original = original.replace(after, before);
  if (hash(original) !== upstreamSHA256)
    throw new Error(
      "Unreviewed pptx-preview source; review Office lifecycle patches before building",
    );
  let patched = original;
  for (const [before, after] of changes) {
    if (patched.split(before).length !== 2)
      throw new Error("Ambiguous Office lifecycle patch");
    patched = patched.replace(before, after);
  }
  return patched;
}
export function patchOfficeViewers() {
  const target = new URL(
    "../node_modules/pptx-preview/dist/pptx-preview.es.js",
    import.meta.url,
  );
  const source = readFileSync(target, "utf8");
  const patched = patchPresentationSource(source);
  if (source !== patched) writeFileSync(target, patched);
}
if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
)
  patchOfficeViewers();
