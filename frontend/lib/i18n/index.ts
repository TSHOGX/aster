"use client";

import { useCallback, useMemo, useSyncExternalStore } from "react";

import { DEFAULT_LOCALE, detectLocale, type Locale } from "./locale";
import { EN, type MessageKey } from "./messages-en";
import { ZH } from "./messages-zh";

export type { Locale, MessageKey };
export { localeInitScript } from "./locale";

const CATALOGS: Record<Locale, Record<MessageKey, string>> = {
  en: EN,
  zh: ZH
};

export type MessageParams = Record<string, string | number>;

/**
 * Substitutes `{name}` placeholders.
 *
 * An unmatched placeholder is left verbatim rather than blanked: `{count} steps`
 * rendering as `{count} steps` is an obvious bug, while ` steps` looks like a
 * plausible string and can ship unnoticed.
 */
export function format(template: string, params?: MessageParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? String(params[name]) : whole
  );
}

/**
 * Splits a template around one `{name}` placeholder, so the caller can render
 * something non-textual in the gap.
 *
 * This exists for strings that embed a styled element — a `/workspace` path in a
 * mono chip, say. Passing such a value through format() would flatten it to plain
 * text, and hand-concatenating the two halves in the component would put a
 * sentence's word order into JSX, which is the one thing a catalog is supposed to
 * own: Chinese and English place the path differently within the sentence.
 *
 * Returns null when the placeholder is absent, letting the caller fall back to
 * the whole string rather than rendering a fragment.
 */
export function splitAround(
  template: string,
  name: string
): { before: string; after: string } | null {
  const token = `{${name}}`;
  const at = template.indexOf(token);
  if (at === -1) return null;
  return {
    before: template.slice(0, at),
    after: template.slice(at + token.length)
  };
}

/** Looks a key up in one locale, falling back to English then to the key. */
export function translate(
  locale: Locale,
  key: MessageKey,
  params?: MessageParams
): string {
  const template = CATALOGS[locale]?.[key] ?? EN[key] ?? key;
  return format(template, params);
}

/**
 * The browser's locale.
 *
 * The server snapshot is the default (English) because SSR has no navigator.
 * That is safe here only because every translated string lives inside a client
 * component that renders after mount — the workbench holds a blank screen until
 * /api/me resolves, so no translated text is in the server HTML to mismatch
 * against. Anything that needs to be correct in SSR output would have to read
 * the Accept-Language header instead.
 */
export function useLocale(): Locale {
  return useSyncExternalStore(
    // The browser's language list does not change during a page's life, so there
    // is nothing to subscribe to — an empty unsubscribe satisfies the contract.
    () => () => {},
    detectLocale,
    () => DEFAULT_LOCALE
  );
}

export type TFunction = (key: MessageKey, params?: MessageParams) => string;

/**
 * Returns the translator for the current locale.
 *
 * Stable across renders for a given locale, so it can be a dependency of memoized
 * children without invalidating them each render.
 */
export function useT(): TFunction {
  const locale = useLocale();
  return useCallback(
    (key: MessageKey, params?: MessageParams) => translate(locale, key, params),
    [locale]
  );
}

/** Both the translator and the active locale, for date/number formatting. */
export function useI18n(): { t: TFunction; locale: Locale } {
  const locale = useLocale();
  const t = useT();
  return useMemo(() => ({ t, locale }), [t, locale]);
}
