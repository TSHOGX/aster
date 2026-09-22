// Markdown AST transform: link prose and inline code, never fenced code or existing links.
type Node = { type: string; value?: string; url?: string; children?: Node[] };
export function workspacePath(
  value: string,
  known: readonly string[] = [],
): string | null {
  let path = value;
  if (path.startsWith("/workspace/")) path = path.slice(11);
  else {
    const matches = known.filter(
      (p) => p === path || p.split("/").at(-1) === path,
    );
    if (matches.length !== 1) return null;
    path = matches[0];
  }
  if (
    !path ||
    path.startsWith("/") ||
    path.split("/").some((p) => p === ".." || p === "") ||
    /[\x00-\x1f]/.test(path)
  )
    return null;
  return path;
}
export function workspaceLinkPlugin(
  options: { known?: readonly string[] } = {},
) {
  return (root: Node) => {
    const walk = (node: Node) => {
      if (
        ["link", "linkReference", "code", "html"].includes(node.type) ||
        !node.children
      )
        return;
      node.children = node.children.flatMap((child) => {
        if (child.type === "inlineCode") {
          const path = workspacePath(child.value || "", options.known);
          return path
            ? [
                {
                  type: "link",
                  url:
                    "/workspace/" +
                    path.split("/").map(encodeURIComponent).join("/"),
                  children: [child],
                },
              ]
            : [child];
        }
        if (child.type === "text") {
          const value = child.value || "",
            parts: Node[] = [];
          let offset = 0;
          for (const match of value.matchAll(
            /\/workspace\/[^\s`"'<>\[\]{}，。；：！？、（）]+/g,
          )) {
            const text = match[0].replace(/[.,;:!?\)]+$/, "");
            const path = workspacePath(text);
            if (!path) continue;
            const start = match.index!;
            if (start > offset)
              parts.push({ type: "text", value: value.slice(offset, start) });
            parts.push({
              type: "link",
              url:
                "/workspace/" +
                path.split("/").map(encodeURIComponent).join("/"),
              children: [{ type: "text", value: text }],
            });
            offset = start + text.length;
          }
          if (!parts.length) return [child];
          if (offset < value.length)
            parts.push({ type: "text", value: value.slice(offset) });
          return parts;
        }
        walk(child);
        return [child];
      });
    };
    walk(root);
  };
}
export function knownWorkspaceFiles(texts: readonly string[]): string[] {
  const paths = new Set<string>();
  for (const text of texts)
    for (const m of text.matchAll(
      /\/workspace\/[^\s`"'<>\[\]{}，。；：！？、（）]+/g,
    )) {
      const path = workspacePath(m[0].replace(/[.,;:!?\)]+$/, ""));
      if (path) paths.add(path);
    }
  return [...paths];
}
