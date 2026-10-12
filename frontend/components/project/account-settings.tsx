"use client";
import { Select } from "@web/select";
import type { Identity } from "@web/identity";

import { useEffect, useRef, useState } from "react";
import { X } from "lucide-react";
import { api, APIError, command } from "@/lib/project-api";
import type { Accent, ThemeMode } from "@web/appearance";
import { useT } from "@/lib/i18n";
import { IconButton, trapDialogFocus, useDialog } from "./ui";
import { AccountAdvanced } from "./account-advanced";
import { AccountUsage } from "./account-usage";
import type { AccountAppearance } from "./account-appearance";
import "./account-settings.css";

type Profile = Pick<Identity, "id" | "username" | "display_name" | "revision">;
export function AccountSettings({ identity, appearance, onIdentity, onProfile, onClose, onLogoutAll }: {
  identity: Identity;
  appearance: AccountAppearance;
  onIdentity: (value: Identity) => void;
  onProfile: (value: Profile) => void;
  onClose: () => void;
  onLogoutAll: () => Promise<void>;
}) {
  const tr = useT(), ask = useDialog();
  const dialog = useRef<HTMLDialogElement>(null);
  const [usageOpen, setUsageOpen] = useState(false);
 const [advancedOpen,setAdvancedOpen]=useState(false),[advancedDirty,setAdvancedDirty]=useState(false);
  const [profile, setProfile] = useState<Profile>(identity);
  const [name, setName] = useState(identity.display_name);
  const [profileLoading, setProfileLoading] = useState(true);
  const [profileBusy, setProfileBusy] = useState(false), [profileError, setProfileError] = useState("");
  const [currentPassword, setCurrentPassword] = useState(""), [newPassword, setNewPassword] = useState("");
  const [securityBusy, setSecurityBusy] = useState(false), [securityError, setSecurityError] = useState(""), [securitySaved, setSecuritySaved] = useState(false);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const node = dialog.current!;
    node.showModal();
    const controller = new AbortController();
    void api<Profile>("/api/me/profile", { signal: controller.signal }).then(value => {
      if (controller.signal.aborted) return;
      setProfile(value); setName(value.display_name);
    }).catch(error => { if (!controller.signal.aborted) setProfileError(String(error)); })
      .finally(() => { if (!controller.signal.aborted) setProfileLoading(false); });
    return () => { controller.abort(); node.close(); previous?.focus(); };
  }, []);
  const discard = async () => {
    if (!advancedDirty && !currentPassword && !newPassword && name === profile.display_name) return true;
    return await ask({ title: tr("Discard unsaved account changes?"),
      description: tr("Unsaved account and advanced settings will be discarded."),
      choices: [{ label: tr("Discard changes"), value: "discard", danger: true }] }) === "discard";
  };
  const close = async () => {
    if (profileBusy || securityBusy) return;
    if (await discard()) onClose();
  };
  const dirtyAppearance = Object.keys(appearance.changes).length > 0;
  return <dialog ref={dialog} className="app-dialog account-settings" aria-labelledby="account-settings-title" onKeyDown={trapDialogFocus}
    onCancel={event => { event.preventDefault(); void close(); }}>
    <header>
      <h2 id="account-settings-title">{tr("Settings")}</h2>
      <IconButton label={tr("Close settings")} onClick={() => void close()} disabled={profileBusy || securityBusy}><X size={18} /></IconButton>
    </header>
    <div className="account-settings-body">
      <section aria-labelledby="appearance-title">
        <h3 id="appearance-title">{tr("Appearance")}</h3>
        <div className="appearance-fields">
          <label>{tr("Theme mode")}
            <Select aria-label={tr("Theme mode")} value={appearance.preview.theme_mode} disabled={!appearance.loaded || appearance.pending}
              onChange={event => void appearance.save({ theme_mode: event.target.value as ThemeMode })}>
              <option value="system">{tr("System")}</option><option value="light">{tr("Light")}</option><option value="dark">{tr("Dark")}</option>
            </Select>
          </label>
          <label>{tr("Accent color")}
            <Select aria-label={tr("Accent color")} value={appearance.preview.accent} disabled={!appearance.loaded || appearance.pending}
              onChange={event => void appearance.save({ accent: event.target.value as Accent })}>
              <option value="neutral">{tr("Neutral")}</option><option value="blue">{tr("Blue")}</option>
              <option value="green">{tr("Green")}</option><option value="violet">{tr("Violet")}</option>
            </Select>
          </label>
        </div>
        <p className="settings-status" role="status">{appearance.pending ? tr("Saving…") : !appearance.loaded ? tr("Loading settings…") : dirtyAppearance ? tr("Unsaved appearance") : appearance.error ? tr("Settings unavailable") : tr("Saved")}</p>
        {appearance.error && <div className="settings-error" role="alert">
          <p>{appearance.conflict ? tr("Settings changed elsewhere. Your preview is unsaved; reapply it or use the saved settings.") : appearance.error}</p>
          <div className="settings-actions">
            <button disabled={appearance.pending} onClick={() => void (dirtyAppearance ? appearance.save() : appearance.refresh())}>{appearance.conflict ? tr("Reapply changes") : tr("Retry")}</button>
            {dirtyAppearance && <button disabled={appearance.pending} onClick={appearance.revert}>{tr("Use saved settings")}</button>}
          </div>
        </div>}
      </section>
      <section><details onToggle={event => setUsageOpen(event.currentTarget.open)}><summary>{tr("Resources and usage")}</summary>{usageOpen && <AccountUsage />}</details></section>
      <section><details onToggle={event=>{if(event.currentTarget.open)setAdvancedOpen(true)}}><summary>{tr("Advanced / Developer")}</summary>{advancedOpen&&<AccountAdvanced onDirty={setAdvancedDirty}/>}</details></section>
      <section aria-labelledby="account-security-title">
        <h3 id="account-security-title">{tr("Account and security")}</h3>
        <form onSubmit={event => {
          event.preventDefault(); setProfileBusy(true); setProfileError("");
          void command<Profile>("/api/me/profile", { expected_revision: profile.revision, display_name: name }, "PATCH")
            .then(value => { setProfile(value); setName(value.display_name); onProfile(value); })
            .catch(async error => {
              if (error instanceof APIError && error.status === 409) {
                try { setProfile(await api<Profile>("/api/me/profile")); } catch {}
                setProfileError(tr("Profile changed elsewhere. Review and save your display name again."));
              } else setProfileError(String(error));
            }).finally(() => setProfileBusy(false));
        }}>
          <label>{tr("Username")}<input readOnly value={profile.username} autoComplete="username" /></label>
          <label>{tr("Display name")}<input required maxLength={256} disabled={profileLoading || profileBusy} value={name} onChange={event => setName(event.target.value)} /></label>
          {profileError && <p className="settings-error" role="alert">{profileError}</p>}
          <button className="primary" disabled={profileLoading || profileBusy || name === profile.display_name}>{profileBusy ? tr("Saving…") : tr("Save display name")}</button>
        </form>
        <form onSubmit={event => {
          event.preventDefault(); setSecurityBusy(true); setSecurityError(""); setSecuritySaved(false);
          void command<Identity>("/api/me/password", { current_password: currentPassword, new_password: newPassword })
            .then(value => { onIdentity(value); setProfile(value); setCurrentPassword(""); setNewPassword(""); setSecuritySaved(true); })
            .catch(error => setSecurityError(String(error))).finally(() => setSecurityBusy(false));
        }}>
          <h4>{tr("Change password")}</h4>
          <label>{tr("Current password")}<input required type="password" autoComplete="current-password" disabled={securityBusy} value={currentPassword} onChange={event => { setCurrentPassword(event.target.value); setSecuritySaved(false); }} /></label>
          <label>{tr("New password")}<input required type="password" autoComplete="new-password" disabled={securityBusy} value={newPassword} onChange={event => { setNewPassword(event.target.value); setSecuritySaved(false); }} /></label>
          <p>{tr("Choose a permanent password with 12 to 128 characters.")}</p>
          <button className="primary" disabled={securityBusy}>{tr("Change password")}</button>
          {securitySaved && <p role="status">{tr("Password changed. Other sessions have been signed out.")}</p>}
        </form>
        {securityError && <p className="settings-error" role="alert">{securityError}</p>}
        <button className="danger" disabled={securityBusy || profileBusy} onClick={() => void (async () => {
          if (!await discard()) return;
          setSecurityBusy(true); setSecurityError("");
          try { await onLogoutAll(); } catch (error) { setSecurityError(String(error)); }
          finally { setSecurityBusy(false); }
        })()}>{tr("Sign out all sessions")}</button>
      </section>
    </div>
  </dialog>;
}
