"use client";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  useCallback,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { MoreHorizontal, X } from "lucide-react";
import { useT } from "@/lib/i18n";

export function IconButton({
  label,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      type="button"
      className="icon-button"
      aria-label={label}
      title={label}
      {...props}
    >
      {children}
    </button>
  );
}
export type MenuItem = {
  label: string;
  action: () => void;
  danger?: boolean;
  disabled?: boolean;
};
export function Menu({
  label,
  items,
  children,
}: {
  label: string;
  items: MenuItem[];
  children?: ReactNode;
}) {
  const [position, setPosition] = useState<{
    top: number;
    left: number;
  } | null>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const close = () => {
    setPosition(null);
    trigger.current?.focus();
  };
  useEffect(() => {
    if (!position) return;
    menu.current
      ?.querySelector<HTMLButtonElement>("button:not(:disabled)")
      ?.focus();
    const outside = (e: PointerEvent) => {
      if (
        !menu.current?.contains(e.target as Node) &&
        !trigger.current?.contains(e.target as Node)
      )
        setPosition(null);
    };
    const dismiss = () => setPosition(null);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", dismiss);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", dismiss);
    };
  }, [position]);
  return (
    <>
      <button
        type="button"
        className={children ? "menu-trigger" : "icon-button menu-trigger"}
        ref={trigger}
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={!!position}
        onClick={() => {
          if (position) {
            close();
            return;
          }
          const rect = trigger.current!.getBoundingClientRect();
          setPosition({
            left: Math.max(8, Math.min(rect.left, window.innerWidth - 248)),
            top: Math.max(
              8,
              Math.min(
                rect.bottom + 4,
                window.innerHeight - Math.min(items.length * 38 + 12, 420) - 8,
              ),
            ),
          });
        }}
      >
        {children || <MoreHorizontal size={18} />}
      </button>
      {position &&
        createPortal(
          <div
            ref={menu}
            className="action-menu"
            role="menu"
            aria-label={label}
            style={position}
            onKeyDown={(e) => {
              if (e.key === "Escape" || e.key === "Tab") {
                if (e.key === "Escape") e.preventDefault();
                close();
              }
              if (["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) {
                e.preventDefault();
                const buttons = [
                  ...menu.current!.querySelectorAll<HTMLButtonElement>(
                    "button:not(:disabled)",
                  ),
                ];
                const i = buttons.indexOf(
                  document.activeElement as HTMLButtonElement,
                );
                buttons[
                  e.key === "Home"
                    ? 0
                    : e.key === "End"
                      ? buttons.length - 1
                      : (i +
                          (e.key === "ArrowDown" ? 1 : -1) +
                          buttons.length) %
                        buttons.length
                ]?.focus();
              }
            }}
          >
            {items.map((item, i) => (
              <button
                type="button"
                role="menuitem"
                key={i}
                disabled={item.disabled}
                className={item.danger ? "danger" : ""}
                onClick={() => {
                  close();
                  item.action();
                }}
              >
                {item.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}
type DialogSpec = {
  title: string;
  description?: string;
  initial?: string;
  input?: boolean;
  choices: { label: string; value: string; danger?: boolean }[];
};
type Ask = (spec: DialogSpec) => Promise<string | null>;
const DialogContext = createContext<Ask>(async () => null);
export function DialogProvider({ children }: { children: ReactNode }) {
  const tr = useT();
  const [spec, setSpec] = useState<DialogSpec | null>(null);
  const resolve = useRef<(value: string | null) => void>(() => {});
  const previous = useRef<HTMLElement | null>(null);
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const done = (value: string | null) => {
    dialog.current?.close();
    resolve.current(value);
    setSpec(null);
    previous.current?.focus();
  };
  useEffect(() => {
    if (spec) {
      dialog.current?.showModal();
      (
        input.current ||
        dialog.current?.querySelector<HTMLButtonElement>("button")
      )?.focus();
    }
  }, [spec]);
  return (
    <DialogContext.Provider
      value={(s) => {
        resolve.current(null);
        previous.current = document.activeElement as HTMLElement;
        setSpec(s);
        return new Promise((r) => {
          resolve.current = r;
        });
      }}
    >
      {children}
      {spec && (
        <dialog
          ref={dialog}
          className="app-dialog"
          aria-labelledby="dialog-title"
          onCancel={(e) => {
            e.preventDefault();
            done(null);
          }}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              done(
                spec.input
                  ? input.current?.value.trim() || null
                  : spec.choices[0].value,
              );
            }}
          >
            <header>
              <h2 id="dialog-title">{spec.title}</h2>
              <IconButton label={tr("Close")} onClick={() => done(null)}>
                <X size={18} />
              </IconButton>
            </header>
            {spec.description && <p>{spec.description}</p>}
            {spec.input && (
              <input
                ref={input}
                aria-label={spec.title}
                defaultValue={spec.initial}
                required
                maxLength={4096}
              />
            )}
            <footer>
              <button type="button" onClick={() => done(null)}>
                {tr("Cancel")}
              </button>
              {spec.choices.map((c) => (
                <button
                  key={c.value}
                  className={c.danger ? "danger" : "primary"}
                  type={spec.input ? "submit" : "button"}
                  onClick={spec.input ? undefined : () => done(c.value)}
                >
                  {c.label}
                </button>
              ))}
            </footer>
          </form>
        </dialog>
      )}
    </DialogContext.Provider>
  );
}
export const useDialog = () => useContext(DialogContext);
export function Splitter({
  label,
  value,
  min,
  max,
  onChange,
  horizontal = false,
  reverse = false,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (n: number) => void;
  horizontal?: boolean;
  reverse?: boolean;
}) {
  const clamp = (n: number) => Math.round(Math.max(min, Math.min(max, n)));
  return (
    <div
      role="separator"
      tabIndex={0}
      aria-label={label}
      aria-orientation={horizontal ? "horizontal" : "vertical"}
      aria-valuenow={Math.round(value)}
      aria-valuemin={min}
      aria-valuemax={Math.round(max)}
      className={`splitter ${horizontal ? "horizontal" : "vertical"}`}
      onKeyDown={(e) => {
        const step = ["ArrowRight", "ArrowDown"].includes(e.key)
          ? 10
          : ["ArrowLeft", "ArrowUp"].includes(e.key)
            ? -10
            : 0;
        if (step) {
          e.preventDefault();
          onChange(clamp(value + step * (reverse ? -1 : 1)));
        }
      }}
      onPointerDown={(e) => {
        e.preventDefault();
        e.currentTarget.setPointerCapture(e.pointerId);
        e.currentTarget.dataset.origin = String(
          horizontal ? e.clientY : e.clientX,
        );
        e.currentTarget.dataset.value = String(value);
      }}
      onPointerMove={(e) => {
        if (!e.currentTarget.hasPointerCapture(e.pointerId)) return;
        onChange(
          clamp(
            Number(e.currentTarget.dataset.value) +
              ((horizontal ? e.clientY : e.clientX) -
                Number(e.currentTarget.dataset.origin)) *
                (reverse ? -1 : 1),
          ),
        );
      }}
      onPointerUp={(e) => {
        if (e.currentTarget.hasPointerCapture(e.pointerId))
          e.currentTarget.releasePointerCapture(e.pointerId);
      }}
    />
  );
}

export function useMediaQuery(query: string) {
  return useSyncExternalStore(
    useCallback(
      (listener) => {
        const m = window.matchMedia(query);
        m.addEventListener("change", listener);
        return () => m.removeEventListener("change", listener);
      },
      [query],
    ),
    () => window.matchMedia(query).matches,
    () => false,
  );
}
