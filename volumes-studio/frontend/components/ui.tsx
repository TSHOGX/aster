"use client";
import type { Message } from "../../../web/i18n/locale";
import { useT } from "../lib/i18n";

import * as Dialog from "@radix-ui/react-dialog";
import * as Dropdown from "@radix-ui/react-dropdown-menu";
import * as Context from "@radix-ui/react-context-menu";
import * as Tooltip from "@radix-ui/react-tooltip";
import { SelectPicker } from "../../../web/select-picker";
import { MoreHorizontal, X } from "lucide-react";
import { ReactNode, useRef, useState } from "react";
export function Modal({
  title,
  children,
  open,
  onClose,
  drawer = false,
  className = "",
}: {
  title: string;
  children: ReactNode;
  open: boolean;
  onClose: () => void;
  drawer?: boolean;
  className?: string;
}) {
  const tr = useT();
  const returnFocus = useRef<HTMLElement | null>(null);
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          data-picker-container
          className={`${drawer ? "dialog drawer" : "dialog"} ${className}`}
          aria-describedby={undefined}
          onOpenAutoFocus={() => {
            returnFocus.current =
              document.activeElement instanceof HTMLElement
                ? document.activeElement
                : null;
          }}
          onCloseAutoFocus={(event) => {
            if (returnFocus.current?.isConnected) {
              event.preventDefault();
              returnFocus.current.focus();
            }
          }}
        >
          <div className="dialog-heading">
            <Dialog.Title>{title}</Dialog.Title>
            <Dialog.Close asChild>
              <button className="icon-button" aria-label={tr("Close dialog")}>
                <X size={18} />
              </button>
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
export type MenuAction = {
  id?: string;
  label: Message;
  action: () => void;
  disabled?: boolean;
  danger?: boolean;
};
export function Menu({
  label = "More actions",
  items,
  children,
}: {
  label?: string;
  items: MenuAction[];
  children?: ReactNode;
}) {
  const tr = useT();
  return (
    <Dropdown.Root>
      <Dropdown.Trigger asChild>
        <button className="icon-button" aria-label={tr(label)} title={tr(label)}>
          {children || <MoreHorizontal size={17} />}
        </button>
      </Dropdown.Trigger>
      <Dropdown.Portal>
        <Dropdown.Content className="menu" sideOffset={5} collisionPadding={8}>
          {items.map((x) => (
            <Dropdown.Item
              className={x.danger ? "danger" : ""}
              key={x.id ?? (typeof x.label === "string" ? x.label : x.label.key)}
              disabled={x.disabled}
              onSelect={x.action}
            >
              {typeof x.label === "string" ? x.label : tr(x.label)}
            </Dropdown.Item>
          ))}
        </Dropdown.Content>
      </Dropdown.Portal>
    </Dropdown.Root>
  );
}
export function ContextMenu({
  items,
  children,
}: {
  items: MenuAction[];
  children: ReactNode;
}) {
  const tr = useT();
  return (
    <Context.Root>
      <Context.Trigger asChild>{children}</Context.Trigger>
      <Context.Portal>
        <Context.Content className="menu" collisionPadding={8}>
          {items.map((x) => (
            <Context.Item
              key={x.id ?? (typeof x.label === "string" ? x.label : x.label.key)}
              disabled={x.disabled}
              className={x.danger ? "danger" : ""}
              onSelect={x.action}
            >
              {typeof x.label === "string" ? x.label : tr(x.label)}
            </Context.Item>
          ))}
        </Context.Content>
      </Context.Portal>
    </Context.Root>
  );
}
export function Tip({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  const tr = useT();
  return (
    <Tooltip.Provider delayDuration={350}>
      <Tooltip.Root>
        <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
        <Tooltip.Portal>
          <Tooltip.Content className="tooltip" sideOffset={6}>
            {tr(label)}
          </Tooltip.Content>
        </Tooltip.Portal>
      </Tooltip.Root>
    </Tooltip.Provider>
  );
}
export function Picker({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  const tr = useT();
  return (
    <SelectPicker
      label={tr(label)}
      value={value}
      options={options.map(option => ({ ...option, label: tr(option.label) }))}
      onChange={onChange}
      showLabel={false}
    />
  );
}
export function TextDialog({
  title,
  label,
  initial = "",
  submit = "Save",
  onClose,
  onSubmit,
}: {
  title: string;
  label: string;
  initial?: string;
  submit?: string;
  onClose: () => void;
  onSubmit: (v: string) => Promise<void>;
}) {
  const tr = useT();
  const [value, setValue] = useState(initial),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Modal
      title={tr(title)}
      open
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (busy) return;
          setError("");
          setBusy(true);
          try {
            await onSubmit(value);
            onClose();
          } catch (e) {
            setError(String(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          {tr(label)}
          <input
            autoFocus
            required
            disabled={busy}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        </label>
        {error && (
          <p role="alert" className="error">
            {tr(error)}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" disabled={busy} onClick={onClose}>
             {tr("Cancel")} </button>
          <button className="primary" disabled={busy || !value.trim()}>
            {tr(submit)}
          </button>
        </div>
      </form>
    </Modal>
  );
}
