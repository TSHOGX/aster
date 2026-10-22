"use client";
import { useEffect } from "react";
import { useLocale } from "./i18n";

// Several independent editors may share one page. Ask once for the page rather
// than prompting separately for each dirty template, profile or workflow.
const editors = new Map<symbol, { message: string; basePath: string }>();
function beforeUnload(event: BeforeUnloadEvent) {
  if (editors.size) { event.preventDefault(); event.returnValue = ""; }
}
function navigate(event: MouseEvent) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const anchor = (event.target as Element | null)?.closest("a");
  if (!anchor || anchor.hasAttribute("download") || anchor.target === "_blank") return;
  const destination = new URL(anchor.href, location.href);
  if (destination.origin === location.origin && destination.pathname === location.pathname && destination.search === location.search) return;
  const editor = editors.values().next().value;
  // Cross-application document navigation is covered by beforeunload. The
  // confirmation here protects client-side routing without a second prompt.
  if (!editor || destination.origin !== location.origin || !destination.pathname.startsWith(editor.basePath + "/")) return;
  if (!window.confirm(editor.message)) { event.preventDefault(); event.stopPropagation(); }
}
export function useUnsavedNavigation(dirty: boolean, basePath: string) {
  const locale = useLocale();
  useEffect(() => {
    if (!dirty) return;
    const key = Symbol();
    if (!editors.size) {
      window.addEventListener("beforeunload", beforeUnload);
      document.addEventListener("click", navigate, true);
    }
    editors.set(key, { message: locale === "zh" ? "放弃未保存的更改？" : "Discard unsaved changes?", basePath });
    return () => {
      editors.delete(key);
      if (!editors.size) {
        window.removeEventListener("beforeunload", beforeUnload);
        document.removeEventListener("click", navigate, true);
      }
    };
  }, [dirty, locale, basePath]);
}
