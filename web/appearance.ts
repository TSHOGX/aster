export type Theme = "light" | "dark";
export type ThemeMode = "system" | Theme;
export type Accent = "neutral" | "blue" | "green" | "violet";
export type Appearance = { theme_mode: ThemeMode; accent: Accent };
export type Preferences = Appearance & { revision: number };
export const DEFAULT_APPEARANCE: Appearance = { theme_mode: "system", accent: "neutral" };
export const ACTIVE_APPEARANCE_KEY = "account-appearance-active";
export const appearanceKey = (id: string) => `account-appearance:${id}`;
const THEME_COLOR: Record<Theme, string> = { light: "#f7f7f8", dark: "#191a1c" };

export function validAppearance(value: unknown): value is Appearance {
  if (!value || typeof value !== "object") return false;
  const fields = value as Record<string, unknown>;
  return typeof fields.theme_mode === "string" && ["system", "light", "dark"].includes(fields.theme_mode) &&
    typeof fields.accent === "string" && ["neutral", "blue", "green", "violet"].includes(fields.accent);
}
export function cachedAppearance(id: string): Preferences | null {
  try {
    const value = JSON.parse(localStorage.getItem(appearanceKey(id)) || "null");
    const revision: unknown = value?.revision;
    if (value?.account_id !== id || typeof revision !== "number" || !Number.isSafeInteger(revision) || revision < 0 || !validAppearance(value)) return null;
    return { theme_mode: value.theme_mode, accent: value.accent, revision };
  } catch { return null; }
}
export function cacheAppearance(id: string, value: Preferences) {
  try {
    const raw = JSON.stringify({ account_id: id, ...value });
    if (localStorage.getItem(appearanceKey(id)) !== raw) localStorage.setItem(appearanceKey(id), raw);
  } catch { /* Server persistence remains authoritative when browser storage is unavailable. */ }
}
export function bindAppearance(id: string) {
  document.documentElement.dataset.accountId = id;
  try { localStorage.setItem(ACTIVE_APPEARANCE_KEY, id); } catch {}
  applyAppearance(cachedAppearance(id) || DEFAULT_APPEARANCE);
}
export function clearAppearanceBinding() {
  delete document.documentElement.dataset.accountId;
  try { localStorage.removeItem(ACTIVE_APPEARANCE_KEY); } catch {}
  applyAppearance(DEFAULT_APPEARANCE);
}
export function applyAppearance(value: Appearance) {
  const theme = value.theme_mode === "system"
    ? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light") : value.theme_mode;
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.accent = value.accent;
  document.documentElement.dataset.themeMode = value.theme_mode;
  document.querySelector('meta[name="theme-color"]:not([media])')?.setAttribute("content", THEME_COLOR[theme]);
}

// Only an explicitly identified account's validated cache may affect first paint.
// Public share pages always start with system/neutral appearance.
export const themeInitScript = `(function(){var p={theme_mode:"system",accent:"neutral"};try{var id=document.documentElement.dataset.accountId;if(id&&!location.pathname.startsWith("/s/")){var v=JSON.parse(localStorage.getItem("account-appearance:"+id)||"null");if(v&&v.account_id===id&&Number.isSafeInteger(v.revision)&&v.revision>=0&&["system","light","dark"].includes(v.theme_mode)&&["neutral","blue","green","violet"].includes(v.accent))p=v;}}catch(e){}var t=p.theme_mode==="system"?(window.matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light"):p.theme_mode;document.documentElement.dataset.theme=t;document.documentElement.dataset.themeMode=p.theme_mode;document.documentElement.dataset.accent=p.accent;var m=document.querySelector('meta[name="theme-color"]:not([media])');if(m)m.setAttribute("content",t==="dark"?"${THEME_COLOR.dark}":"${THEME_COLOR.light}");})();`;
