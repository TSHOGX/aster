"use client";
import { useLocale } from "./i18n";
import { Select } from "./select";
import type { AccountPreferencesController } from "./account-preferences";
import type { Accent, ThemeMode } from "./appearance";

export function AppearanceFields({ preferences }: { preferences: AccountPreferencesController }) {
  const zh = useLocale() === "zh";
  return <div className="appearance-fields">
    <label>{zh ? "主题模式" : "Theme mode"}
      <Select aria-label={zh ? "主题模式" : "Theme mode"} value={preferences.preview.theme_mode} disabled={!preferences.loaded || preferences.pending}
        onChange={event => void preferences.save({ theme_mode: event.target.value as ThemeMode })}>
        <option value="system">{zh ? "跟随系统" : "System"}</option><option value="light">{zh ? "浅色" : "Light"}</option><option value="dark">{zh ? "深色" : "Dark"}</option>
      </Select>
    </label>
    <label>{zh ? "强调色" : "Accent color"}
      <Select aria-label={zh ? "强调色" : "Accent color"} value={preferences.preview.accent} disabled={!preferences.loaded || preferences.pending}
        onChange={event => void preferences.save({ accent: event.target.value as Accent })}>
        <option value="neutral">{zh ? "中性" : "Neutral"}</option><option value="blue">{zh ? "蓝色" : "Blue"}</option>
        <option value="green">{zh ? "绿色" : "Green"}</option><option value="violet">{zh ? "紫色" : "Violet"}</option>
      </Select>
    </label>
  </div>;
}
