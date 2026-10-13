"use client";
import { useLocale } from "./i18n";
import type { Language } from "./i18n/locale";
import type { AccountPreferencesController } from "./account-preferences";
import { Select } from "./select";
export function LanguagePicker({ preferences, feedback = true }: { preferences: AccountPreferencesController; feedback?: boolean }) {
  const zh = useLocale() === "zh";
  const dirty = Object.keys(preferences.changes).length > 0;
  return <div className="language-setting">
    <label>{zh ? "语言" : "Language"}
      <Select aria-label={zh ? "语言" : "Language"} value={preferences.preview.language}
        disabled={!preferences.loaded || preferences.pending}
        onChange={event => void preferences.save({ language: event.target.value as Language })}>
        <option value="system">{zh ? "跟随浏览器" : "Follow browser"}</option>
        <option value="zh">{zh ? "中文（简体）" : "Chinese (Simplified)"}</option><option value="en">English</option>
      </Select>
    </label>
    {feedback && (preferences.pending || dirty) && <span role="status">{preferences.pending ? zh ? "正在保存…" : "Saving…" : dirty ? zh ? "更改尚未保存" : "Unsaved changes" : ""}</span>}
    {feedback && preferences.error && <div role="alert">
      <p>{preferences.conflict ? zh ? "设置已在其他位置更改。请重新应用预览或使用已保存的设置。" : "Settings changed elsewhere. Reapply your preview or use saved settings." : zh ? "设置保存失败。请重试。" : "Settings could not be saved. Please retry."}</p>
      <button disabled={preferences.pending} onClick={() => void (dirty ? preferences.save() : preferences.refresh())}>{zh ? "重试" : "Retry"}</button>
      {dirty && <button disabled={preferences.pending} onClick={preferences.revert}>{zh ? "使用已保存的设置" : "Use saved settings"}</button>}
    </div>}
  </div>;
}
