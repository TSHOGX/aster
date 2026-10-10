/**
 * Locale resolution from the browser's own language preference.
 *
 * Two locales only: Chinese for a Chinese browser, English for everything else.
 * There is no in-app language switcher by design — the browser already carries
 * the user's answer to this question, and a second control that can disagree
 * with it is a bug surface, not a feature.
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

/**
 * Sets `<html lang>` before first paint, mirroring web/appearance.ts's init script.
 *
 * The attribute matters beyond tidiness: it is what screen readers use to pick a
 * pronunciation voice, and what the browser's own translate prompt reads. The
 * server renders `lang="en"`, so without this a Chinese user's page would claim
 * to be English while displaying Chinese text.
 */
export const localeInitScript = `(function(){try{var n=navigator.languages&&navigator.languages.length?navigator.languages:(navigator.language?[navigator.language]:[]);var l="en";for(var i=0;i<n.length;i++){var t=String(n[i]||"").toLowerCase().replace(/_/g,"-").split("-")[0];if(t){l=(t==="zh")?"zh":"en";break;}}document.documentElement.setAttribute("lang",l==="zh"?"zh-Hans":"en");}catch(e){}})();`;
