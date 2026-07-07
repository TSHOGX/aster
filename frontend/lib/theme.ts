export type Theme = "light" | "dark";

export const THEME_STORAGE_KEY = "art-theme";

/** Page background per theme; mirrors --bg in globals.css. Used for mobile chrome. */
const THEME_COLOR: Record<Theme, string> = {
  light: "#f7f6f2",
  dark: "#1e1e2e"
};

/** Persist a user-selected theme and reflect it onto <html> so the CSS variables switch. */
export function setTheme(theme: Theme) {
  window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  document.documentElement.setAttribute("data-theme", theme);
  // Keep mobile browser chrome in sync with the in-app choice (the static
  // viewport.themeColor only tracks the OS preference).
  const meta = document.querySelector('meta[name="theme-color"]:not([media])');
  if (meta) meta.setAttribute("content", THEME_COLOR[theme]);
}

/**
 * Inline <head> script (stringified) that sets data-theme before first paint,
 * preventing a light-mode flash for dark-theme users. Kept dependency-free and
 * self-contained since it runs before any bundle loads.
 */
export const themeInitScript = `(function(){try{var k="${THEME_STORAGE_KEY}";var s=localStorage.getItem(k);var t=(s==="light"||s==="dark")?s:(window.matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light");document.documentElement.setAttribute("data-theme",t);var m=document.querySelector('meta[name="theme-color"]:not([media])');if(m)m.setAttribute("content",t==="dark"?"${THEME_COLOR.dark}":"${THEME_COLOR.light}");}catch(e){}})();`;
