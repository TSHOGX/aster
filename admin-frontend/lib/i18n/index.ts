"use client";
import { useTranslator } from "../../../web/i18n";
import type { Locale, Message, MessageParams } from "../../../web/i18n/locale";
import { translateText } from "../../../web/i18n/text";
import { ZH } from "./messages";
export { message } from "../../../web/i18n/locale";
export function translate(locale: Locale, key: Message | undefined, params?: MessageParams) { return translateText(locale, key, params, ZH); }
export function useT() { return useTranslator(ZH); }
