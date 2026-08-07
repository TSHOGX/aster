/**
 * Which workspace files the agent's latest reply refers to.
 *
 * The hard part is not finding path-shaped text, it is *not* finding it: a naive
 * path regex reads `v1.2`, `3.5%` and `e.g.` as filenames. So this never guesses
 * from shape alone — the session's real artifact list is a whitelist, and a token
 * counts as a reference only when it resolves to a file that actually exists.
 * Precision comes from that intersection, and every path this returns is
 * guaranteed to open.
 *
 * The trade-off is deliberate: a file the reply mentions but never wrote is
 * dropped, which is the direction we want a UI affordance to fail in.
 */

/** Upper bound on how many references one reply contributes. */
export const MENTION_CAP = 12;

/**
 * Token boundaries. Notably absent: `.` `-` `_` `/`, which are path characters —
 * a trailing sentence dot is stripped afterwards instead. Includes CJK
 * punctuation, since replies are written in Chinese as often as English.
 *
 * The fullwidth brackets are not decoration: a Chinese reply writes
 * `报告（out/report.html）已生成` or `《out/report.html》` far more often than the
 * ASCII forms, and treating only `(` `)` as boundaries left the path fused to its
 * bracket and unresolvable. `=` earns its place the same way, via `file=out/x.html`.
 * Ideographic space (U+3000) and NBSP fall out of `\s` already.
 */
const SEPARATORS =
  /[\s`"'*<>|()[\]{}=、，,。;；:：!！?？「」『』【】〈〉《》（）“”‘’~…]+/;

export type PathIndex = {
  /** Lowercased full artifact path → the artifact's real path. */
  byPath: Map<string, string>;
  /** Lowercased basename → real path, or null when two files share the name. */
  byName: Map<string, string | null>;
};

/**
 * All this needs is a path. Kept structural rather than taking `ApiArtifact` so
 * the public-link page can pass its own `SharedLinkFile` (path + size only,
 * built field-by-field on the server) without a cast.
 */
type PathBearing = { path: string };

export function buildPathIndex(artifacts: readonly PathBearing[]): PathIndex {
  const byPath = new Map<string, string>();
  const byName = new Map<string, string | null>();
  for (const artifact of artifacts) {
    const path = artifact.path;
    if (!path) continue;
    byPath.set(path.toLowerCase(), path);
    const name = path.split("/").pop();
    if (!name) continue;
    const key = name.toLowerCase();
    // Second sighting of a basename makes it ambiguous forever. Marking it null
    // rather than keeping the first is the point: with `outputs/report.md` and
    // `archive/report.md` both present, a bare "report.md" in prose cannot be
    // resolved, and opening the wrong one is worse than showing neither.
    byName.set(key, byName.has(key) ? null : path);
  }
  return { byPath, byName };
}

/**
 * Strips the packaging around a path so it can be looked up: markdown leftovers
 * the splitter cannot see, and the several ways a reply may anchor a path.
 */
function normalizeToken(raw: string): string {
  let token = raw.trim();
  // A sentence-ending dot survives tokenization because `.` is a path character.
  token = token.replace(/[.·]+$/, "");
  token = token.replace(/^~\//, "");
  token = token.replace(/^\.\//, "");
  // Agents cite either the workspace-relative path or the absolute one; the
  // artifact list stores the former, so fold the mount prefix away.
  token = token.replace(/^\/?workspace\//, "");
  token = token.replace(/^\/+/, "");
  return token;
}

/**
 * Artifact paths mentioned in `content`, in order of first appearance.
 *
 * First-appearance order is the agent's own narrative order, which is the order
 * the user just read them in — a better match for "what did this reply produce"
 * than sorting by mtime or name.
 */
export function extractMentionedPaths(
  content: string,
  index: PathIndex,
  cap: number = MENTION_CAP
): string[] {
  if (!content) return [];
  const found: string[] = [];
  const seen = new Set<string>();

  for (const raw of content.split(SEPARATORS)) {
    if (!raw) continue;
    const token = normalizeToken(raw);
    if (!token) continue;
    const key = token.toLowerCase();

    // A token carrying no directory has to look like a filename to be considered
    // at all — by full path or by basename. Without the dot requirement an
    // artifact innocently named `data` or `notes` (which lives at the workspace
    // root, so its path *is* its basename) would match every prose use of the
    // word. A slash is evidence enough on its own, so paths keep matching.
    const pathLike = token.includes("/") || token.includes(".");
    if (!pathLike) continue;

    const hit = index.byPath.get(key) ?? index.byName.get(key) ?? undefined;
    if (!hit || seen.has(hit)) continue;

    seen.add(hit);
    found.push(hit);
    if (found.length >= cap) break;
  }
  return found;
}
