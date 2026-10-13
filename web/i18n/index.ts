"use client";
import { useSyncExternalStore } from "react";
import { currentLocale, DEFAULT_LOCALE, subscribeLocale } from "./locale";
export * from "./locale";
export function useLocale() { return useSyncExternalStore(subscribeLocale, currentLocale, () => DEFAULT_LOCALE); }
