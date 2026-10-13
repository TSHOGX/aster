import type { Entry } from "./api";
export type Page<T> = {
  items: T[];
  next: string;
  total: number;
  revision: number;
  base_version: string;
};
export const query = (values: Record<string, string | number | undefined>) =>
  new URLSearchParams(
    Object.entries(values)
      .filter(([, v]) => v !== undefined)
      .map(([k, v]) => [k, String(v)]),
  ).toString();
export const parentPath = (path: string) =>
  path.split("/").slice(0, -1).join("/");
export const entryIdentity = (entry: Entry | null) =>
  entry ? JSON.stringify([entry.kind, entry.size, entry.digest]) : "absent";
export type TextFormat = {
  bom: boolean;
  separator: "\n" | "\r\n";
  mixed: boolean;
};
export function decodeText(data: ArrayBuffer): {
  text: string;
  format: TextFormat;
} {
  const raw = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
    data,
  );
  if (raw.includes("\0")) throw new Error("This file is not editable text.");
  const bom = raw.startsWith("\uFEFF");
  const source = bom ? raw.slice(1) : raw;
  const crlf = (source.match(/\r\n/g) || []).length;
  const lf = (source.match(/(?<!\r)\n/g) || []).length;
  return {
    text: source.replace(/\r\n/g, "\n"),
    format: {
      bom,
      separator: crlf && !lf ? "\r\n" : "\n",
      mixed: !!crlf && !!lf,
    },
  };
}
export function encodeText(text: string, format: TextFormat) {
  return (format.bom ? "\uFEFF" : "") + text.replace(/\n/g, format.separator);
}
export class WriteQueue {
  private tails = new Map<string, Promise<unknown>>();
  run<T>(key: string, action: () => Promise<T>): Promise<T> {
    const previous = this.tails.get(key) || Promise.resolve();
    const next = previous.catch(() => {}).then(action);
    this.tails.set(key, next);
    void next
      .finally(() => {
        if (this.tails.get(key) === next) this.tails.delete(key);
      })
      .catch(() => {});
    return next;
  }
}
export function relativeTime(seconds?: number | null, locale = "en") {
  if (!seconds) return "—";
  const d = Math.round((seconds * 1000 - Date.now()) / 1000);
  const f = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  if (Math.abs(d) < 60) return f.format(d, "second");
  if (Math.abs(d) < 3600) return f.format(Math.round(d / 60), "minute");
  if (Math.abs(d) < 86400) return f.format(Math.round(d / 3600), "hour");
  return f.format(Math.round(d / 86400), "day");
}

export function storageRead(
  kind: "local" | "session",
  key: string,
): string | null {
  try {
    return (kind === "local" ? localStorage : sessionStorage).getItem(key);
  } catch {
    return null;
  }
}
export function storageWrite(
  kind: "local" | "session",
  key: string,
  value: string,
): void {
  try {
    (kind === "local" ? localStorage : sessionStorage).setItem(key, value);
  } catch {
    /* Layout preferences are optional; recovery storage has its own visible errors. */
  }
}
