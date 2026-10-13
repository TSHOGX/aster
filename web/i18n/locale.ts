/**
 * Locale resolution from the browser's own language preference.
 *
 * Two locales only: Chinese for a Chinese browser, English for everything else.
 * Account preferences can override the browser language.
 */

export type Locale = "en" | "zh";

export const DEFAULT_LOCALE: Locale = "en";

/**
 * True for any Chinese BCP-47 tag: `zh`, `zh-CN`, `zh-TW`, `zh-Hans`,
 * `zh-Hant-HK`, and so on.
 *
 * Matching the `zh` primary subtag rather than an allow-list of regions is what
 * makes this hold up: a tag like `zh-SG` or `zh-Hans-MO` is still a user asking
 * for Chinese, and an allow-list would silently serve them English. We do not
 * distinguish Simplified from Traditional — one Chinese catalog (Simplified),
 * matching the product's existing operator docs.
 */
export function isChineseTag(tag: string): boolean {
  // Case-insensitive, and tolerant of the underscore form (`zh_CN`) that some
  // environments produce instead of the hyphenated BCP-47 form.
  const primary = tag.toLowerCase().replace(/_/g, "-").split("-")[0];
  return primary === "zh";
}

/**
 * Picks a locale from an ordered list of language tags, most-preferred first
 * (the shape of `navigator.languages`).
 *
 * The whole list is scanned rather than just the first entry: a browser set to
 * `["en-US", "zh-CN"]` is a user who reads English first, but `["zh-CN", "en"]`
 * and `["en", "zh-CN"]` are genuinely different preferences and only respecting
 * position gets both right. First Chinese tag encountered before any non-Chinese
 * one wins.
 */
export function resolveLocale(tags: readonly string[] | undefined): Locale {
  if (!tags || tags.length === 0) return DEFAULT_LOCALE;
  for (const tag of tags) {
    if (typeof tag !== "string" || tag.trim() === "") continue;
    // The first tag that expresses *any* concrete preference decides, so an
    // explicit English-before-Chinese order is honoured rather than overridden
    // by a Chinese tag further down the list.
    return isChineseTag(tag) ? "zh" : DEFAULT_LOCALE;
  }
  return DEFAULT_LOCALE;
}

/** Reads the live browser preference. Client-only; guards for SSR. */
export function detectLocale(): Locale {
  if (typeof navigator === "undefined") return DEFAULT_LOCALE;
  // `languages` is the full ordered list; `language` is the single top choice and
  // exists in every browser, so it is the fallback.
  const tags =
    Array.isArray(navigator.languages) && navigator.languages.length > 0
      ? navigator.languages
      : navigator.language
        ? [navigator.language]
        : [];
  return resolveLocale(tags);
}

export type Language = "system" | Locale;
export function validLanguage(value: unknown): value is Language {
  return value === "system" || value === "en" || value === "zh";
}
let activeLanguage: Language = "system";
const listeners = new Set<() => void>();
export function currentLocale(): Locale {
  return activeLanguage === "system" ? detectLocale() : activeLanguage;
}
export function setLanguage(value: Language) {
  activeLanguage = value;
  if (typeof document !== "undefined") document.documentElement.lang = currentLocale() === "zh" ? "zh-Hans" : "en";
  listeners.forEach(listener => listener());
}
function browserLanguageChanged() { if (activeLanguage === "system") setLanguage("system"); }
export function subscribeLocale(listener: () => void) {
  listeners.add(listener);
  if (listeners.size === 1) window.addEventListener("languagechange", browserLanguageChanged);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) window.removeEventListener("languagechange", browserLanguageChanged);
  };
}
export function intlLocale(locale: Locale = currentLocale()) { return locale === "zh" ? "zh-CN" : "en-US"; }
export function format(template: string, params?: Record<string, string | number>): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => Object.hasOwn(params, name) ? String(params[name]) : whole);
}
export const localeInitScript = `(function(){try{var n=navigator.languages&&navigator.languages.length?navigator.languages:(navigator.language?[navigator.language]:[]);var l="en";for(var i=0;i<n.length;i++){var t=String(n[i]||"").toLowerCase().replace(/_/g,"-").split("-")[0];if(t){l=t==="zh"?"zh":"en";break;}}var id=document.documentElement.dataset.accountId;if(id&&!location.pathname.startsWith("/s/")){var v=JSON.parse(localStorage.getItem("account-appearance:"+id)||"null");if(v&&v.account_id===id&&Number.isSafeInteger(v.revision)&&v.revision>=0&&["system","light","dark"].includes(v.theme_mode)&&["neutral","blue","green","violet"].includes(v.accent)&&(v.language==="zh"||v.language==="en"))l=v.language;}document.documentElement.lang=l==="zh"?"zh-Hans":"en";}catch(e){}})();`;

export type MessageParams = Record<string, string | number>;
export type Message = string | { key: string; params?: MessageParams };
export function message(key: string, params?: MessageParams): Message { return { key, params }; }

/** Display quantities without changing their serialized values or losing integer precision. */
export function formatQuantity(value: string | number | null | undefined): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string" && /^\d+$/.test(value)) return new Intl.NumberFormat(intlLocale()).format(BigInt(value));
  return typeof value === "number" ? new Intl.NumberFormat(intlLocale()).format(value) : value;
}
