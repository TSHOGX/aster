import { afterEach, expect, it, vi } from "vitest";
import { runInNewContext } from "node:vm";
import { ACTIVE_APPEARANCE_KEY, appearanceKey, cachedAppearance, themeInitScript, validAppearance } from "../../web/appearance";

afterEach(() => vi.unstubAllGlobals());
it("requires a supported cache bound to the exact immutable account", () => {
  const values = new Map<string, string>();
  vi.stubGlobal("localStorage", { getItem: (key: string) => values.get(key) || null });
  values.set(appearanceKey("b"), JSON.stringify({ account_id: "a", revision: 3, theme_mode: "dark", accent: "violet" }));
  expect(cachedAppearance("b")).toBeNull();
  values.set(appearanceKey("b"), JSON.stringify({ account_id: "b", revision: 3, theme_mode: "dark", accent: "violet" }));
  expect(cachedAppearance("b")).toEqual({ revision: 3, theme_mode: "dark", accent: "violet" });
  expect(validAppearance({ theme_mode: ["dark"], accent: "blue" })).toBe(false);
  expect(validAppearance({ theme_mode: "system", accent: "red" })).toBe(false);
});
it("first paint uses only identified-account preferences and leaves public pages anonymous", () => {
  const values = new Map<string, string>([
    [ACTIVE_APPEARANCE_KEY, "b"],
    [appearanceKey("a"), JSON.stringify({ account_id: "a", revision: 1, theme_mode: "dark", accent: "violet" })],
  ]);
  const render = (pathname: string, account?: string) => {
    const dataset: Record<string, string> = account ? { accountId: account } : {};
    runInNewContext(themeInitScript, {
      localStorage: { getItem: (key: string) => values.get(key) || null },
      location: { pathname }, window: { matchMedia: () => ({ matches: false }) },
      document: { documentElement: { dataset }, querySelector: () => null },
    });
    delete dataset.accountId;
    return dataset;
  };
  expect(render("/")).toEqual({ theme: "light", themeMode: "system", accent: "neutral" });
  values.set(appearanceKey("b"), JSON.stringify({ account_id: "b", revision: 2, theme_mode: "dark", accent: "blue" }));
  expect(render("/", "b")).toEqual({ theme: "dark", themeMode: "dark", accent: "blue" });
  expect(render("/s/public", "b")).toEqual({ theme: "light", themeMode: "system", accent: "neutral" });
  values.delete(ACTIVE_APPEARANCE_KEY);
  expect(render("/")).toEqual({ theme: "light", themeMode: "system", accent: "neutral" });
});
