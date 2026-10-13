// Login returns are confined to administration and the registered identity flow.
export function loginReturnPath(value: string | null, origin: string): string | null {
  if (!value || value.length > 4096 || !value.startsWith("/") || value.startsWith("//") || value.includes("\\") || /[\x00-\x1f\x7f]/.test(value)) return null;
  try {
    const target = new URL(value, origin);
    if (target.origin !== origin) return null;
    const path = value.split(/[?#]/, 1)[0];
    if (target.pathname !== path) return null;
    const admin = /^\/admin(?:\/[a-zA-Z0-9_-]+)*\/?$/.test(path);
    const publicShare = /^\/s\/[a-zA-Z0-9_-]{43}$/.test(path);
    if (!admin && !publicShare && path !== "/api/identity/v1/authorize") return null;
    return target.pathname + target.search + target.hash;
  } catch { return null; }
}
