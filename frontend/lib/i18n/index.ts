"use client";

import { useCallback } from "react";

import { type Locale } from "./locale";
import { useLocale } from "@web/i18n";
export { useLocale } from "@web/i18n";
import { type MessageKey } from "./messages-en";
import { translateText } from "@web/i18n/text";
import { ZH } from "./messages-zh";

export type { Locale, MessageKey };
export { localeInitScript } from "./locale";

export type MessageParams = Record<string, string | number>;

/**
 * Substitutes `{name}` placeholders.
 *
 * An unmatched placeholder is left verbatim rather than blanked: `{count} steps`
 * rendering as `{count} steps` is an obvious bug, while ` steps` looks like a
 * plausible string and can ship unnoticed.
 */
export { format } from "@web/i18n/locale";

/** Looks a key up in one locale, falling back to English then to the key. */
export function translate(
  locale: Locale,
  key: string,
  params?: MessageParams,
): string {
  return translateText(locale, key, params, ZH);
}

export type TFunction = (key: string, params?: MessageParams) => string;

/**
 * Returns the translator for the current locale.
 *
 * Stable across renders for a given locale, so it can be a dependency of memoized
 * children without invalidating them each render.
 */
export function useT(): TFunction {
  const locale = useLocale();
  return useCallback(
    (key: string, params?: MessageParams) => translate(locale, key, params),
    [locale],
  );
}
