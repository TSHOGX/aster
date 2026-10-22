"use client";
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";

// A confirmation request. `impact` lists consequences; `reason` asks for a
// rationale and returns it. Text is translated by the caller.
export type ConfirmRequest = {
  title: string;
  body?: ReactNode;
  impact?: ReactNode[];
  confirm: string;
  cancel: string;
  danger?: boolean;
  reason?: { label: string; required?: boolean; placeholder?: string };
};
export type ConfirmResult = { confirmed: boolean; reason: string };
type Ask = (request: ConfirmRequest) => Promise<ConfirmResult>;

const ConfirmContext = createContext<Ask | null>(null);

// ConfirmProvider renders one native <dialog> per request: it traps focus,
// restores focus on close, closes on Escape and can be styled and translated,
// unlike window.confirm.
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [request, setRequest] = useState<(ConfirmRequest & { resolve: (r: ConfirmResult) => void }) | null>(null);
  const [reason, setReason] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const ask = useCallback<Ask>(value => new Promise(resolve => {
    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setReason("");
    setRequest({ ...value, resolve });
  }), []);
  useEffect(() => {
    const node = dialog.current;
    if (request && node && !node.open) node.showModal();
  }, [request]);
  const finish = (confirmed: boolean) => {
    if (!request) return;
    const value = { confirmed, reason: reason.trim() };
    request.resolve(value);
    dialog.current?.close();
    setRequest(null);
    opener.current?.focus();
  };
  const blocked = !!request?.reason?.required && !reason.trim();
  return <ConfirmContext.Provider value={ask}>
    {children}
    {request && <dialog ref={dialog} className="confirm-dialog" aria-labelledby="confirm-title" onCancel={event => { event.preventDefault(); finish(false); }}>
      <form method="dialog" onSubmit={event => { event.preventDefault(); if (!blocked) finish(true); }}>
        <h2 id="confirm-title">{request.title}</h2>
        {request.body && <p>{request.body}</p>}
        {request.impact && request.impact.length > 0 && <ul>{request.impact.map((item, index) => <li key={index}>{item}</li>)}</ul>}
        {request.reason && <label>{request.reason.label}<textarea autoFocus value={reason} placeholder={request.reason.placeholder} onChange={event => setReason(event.target.value)} /></label>}
        <div className="button-row">
          <button type="button" className="secondary" autoFocus={!request.reason} onClick={() => finish(false)}>{request.cancel}</button>
          <button type="submit" className={request.danger ? "danger solid" : "primary"} disabled={blocked}>{request.confirm}</button>
        </div>
      </form>
    </dialog>}
  </ConfirmContext.Provider>;
}

// useConfirm returns an async confirm. Outside a provider it falls back to the
// native dialog so components stay usable in isolation and tests.
export function useConfirm(): Ask {
  const ask = useContext(ConfirmContext);
  return ask ?? (async request => {
    const confirmed = typeof window !== "undefined" && window.confirm([request.title, typeof request.body === "string" ? request.body : ""].filter(Boolean).join("\n\n"));
    return { confirmed, reason: "" };
  });
}
