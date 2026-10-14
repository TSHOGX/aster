"use client";
import { useCallback, useSyncExternalStore } from "react";
import { currentLocale, DEFAULT_LOCALE, subscribeLocale, type Message, type MessageParams } from "./locale";
import { translateText, type MessageCatalog } from "./text";
export * from "./locale";
export function useLocale() { return useSyncExternalStore(subscribeLocale, currentLocale, () => DEFAULT_LOCALE); }

/** Keep translated copy reactive without replacing editors or stored user input. */
const EMPTY_CATALOG: MessageCatalog = {};
export function useTranslator(catalog: MessageCatalog, english: MessageCatalog = EMPTY_CATALOG) {
  const locale = useLocale();
  return useCallback(
    (key: Message | undefined, params?: MessageParams) => translateText(locale, key, params, catalog, english),
    [locale, catalog, english],
  );
}
