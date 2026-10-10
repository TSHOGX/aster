"use client";
import { useEffect, useRef, useState } from "react";
export function CredentialDialog({ username, password, onClose }: { username: string; password: string; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null), field = useRef<HTMLInputElement>(null);
  const [message, setMessage] = useState("");
  useEffect(() => { const previous = document.activeElement as HTMLElement | null; const node = ref.current!; node.showModal(); field.current?.focus(); return () => { node.close(); previous?.focus(); }; }, []);
  return <dialog ref={ref} aria-labelledby="credential-title" onCancel={event => { event.preventDefault(); onClose(); }} onKeyDown={event => {
    if (event.key !== "Tab") return;
    const controls = [...event.currentTarget.querySelectorAll<HTMLElement>("input,button")];
    if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls.at(-1)?.focus(); }
    else if (!event.shiftKey && document.activeElement === controls.at(-1)) { event.preventDefault(); controls[0].focus(); }
  }}>
    <h2 id="credential-title">Temporary password for {username}</h2>
    <p>This password is shown once. It expires after 24 hours and can be used for one sign-in to set a permanent password.</p>
    <label>Temporary password<input ref={field} readOnly autoComplete="off" spellCheck={false} value={password} onFocus={event => event.target.select()} /></label>
    <p>After closing this dialog, issue a new reset if another password is needed.</p>
    {message && <p role="status">{message}</p>}
    <footer><button onClick={() => { if (!navigator.clipboard) { setMessage("Select and copy the password manually."); return; } void navigator.clipboard.writeText(password).then(() => setMessage("Copied.")).catch(() => setMessage("Copy is unavailable. Select and copy the password manually.")); }}>Copy password</button><button className="primary" onClick={onClose}>Done</button></footer>
  </dialog>;
}
