"use client";

import { useCallback, useSyncExternalStore } from "react";

import { DEFAULT_LOCALE, detectLocale, type Locale } from "./locale";
import { EN, type MessageKey } from "./messages-en";
import { ZH } from "./messages-zh";

export type { Locale, MessageKey };
export { localeInitScript } from "./locale";

const CATALOGS: Record<Locale, Record<MessageKey, string>> = {
  en: EN,
  zh: ZH,
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
    name in params ? String(params[name]) : whole,
  );
}

/** Looks a key up in one locale, falling back to English then to the key. */
export function translate(
  locale: Locale,
  key: MessageKey,
  params?: MessageParams,
): string {
  const template = CATALOGS[locale]?.[key] ?? EN[key] ?? key;
  return format(template, params);
}

// Hydration starts from the English server snapshot, then React applies the
// detected browser preference without mismatching server-rendered markup.
export function useLocale(): Locale {
  return useSyncExternalStore(
    // The browser's language list does not change during a page's life, so there
    // is nothing to subscribe to — an empty unsubscribe satisfies the contract.
    () => () => {},
    detectLocale,
    () => DEFAULT_LOCALE,
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
    [locale],
  );
}
