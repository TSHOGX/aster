// Only canonical local administration destinations are valid login returns.
export function adminReturnPath(value: string | null, origin: string): string | null {
  if (!value || value.length > 4096 || !value.startsWith("/") || value.startsWith("//") || value.includes("\\") || /[\x00-\x1f\x7f]/.test(value)) return null;
  try {
    const target = new URL(value, origin);
    if (target.origin !== origin || !/^\/admin(?:\/[a-zA-Z0-9_-]+)*\/?$/.test(target.pathname)) return null;
    return target.pathname + target.search + target.hash;
  } catch { return null; }
}
