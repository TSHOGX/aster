"use client";
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
              <button className="icon-button" aria-label="Close dialog">
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
  label: string;
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
  return (
    <Dropdown.Root>
      <Dropdown.Trigger asChild>
        <button className="icon-button" aria-label={label} title={label}>
          {children || <MoreHorizontal size={17} />}
        </button>
      </Dropdown.Trigger>
      <Dropdown.Portal>
        <Dropdown.Content className="menu" sideOffset={5} collisionPadding={8}>
          {items.map((x) => (
            <Dropdown.Item
              className={x.danger ? "danger" : ""}
              key={x.label}
              disabled={x.disabled}
              onSelect={x.action}
            >
              {x.label}
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
  return (
    <Context.Root>
      <Context.Trigger asChild>{children}</Context.Trigger>
      <Context.Portal>
        <Context.Content className="menu" collisionPadding={8}>
          {items.map((x) => (
            <Context.Item
              key={x.label}
              disabled={x.disabled}
              className={x.danger ? "danger" : ""}
              onSelect={x.action}
            >
              {x.label}
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
  return (
    <Tooltip.Provider delayDuration={350}>
      <Tooltip.Root>
        <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
        <Tooltip.Portal>
          <Tooltip.Content className="tooltip" sideOffset={6}>
            {label}
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
  return (
    <SelectPicker
      label={label}
      value={value}
      options={options}
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
  const [value, setValue] = useState(initial),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Modal
      title={title}
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
          {label}
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
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={busy || !value.trim()}>
            {submit}
          </button>
        </div>
      </form>
    </Modal>
  );
}
