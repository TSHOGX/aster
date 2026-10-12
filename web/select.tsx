"use client";

import {
  Children,
  isValidElement,
  useRef,
  useState,
  type ReactNode,
  type SelectHTMLAttributes,
} from "react";
import { SelectPicker, type PickerOption } from "./select-picker";

type OptionProps = {
  children?: ReactNode;
  value?: string | number;
  label?: string;
  disabled?: boolean;
};
function textContent(node: ReactNode): string {
  return Children.toArray(node)
    .map((child) =>
      isValidElement<OptionProps>(child)
        ? textContent(child.props.children)
        : String(child),
    )
    .join("");
}
function optionsFrom(children: ReactNode, disabled = false): PickerOption[] {
  return Children.toArray(children).flatMap((child) => {
    if (!isValidElement<OptionProps>(child)) return [];
    if (child.type === "option") {
      const label = child.props.label || textContent(child.props.children);
      return [
        {
          value: String(child.props.value ?? label),
          label,
          disabled: disabled || child.props.disabled,
        },
      ];
    }
    return optionsFrom(child.props.children, disabled || child.props.disabled);
  });
}

type SelectProps = Omit<
  SelectHTMLAttributes<HTMLSelectElement>,
  "value" | "defaultValue" | "multiple" | "size"
> & {
  value: string;
  "aria-label": string;
};

// A system-styled picker backed by a native form control for serialization,
// constraint validation and React change events. The native menu is never shown.
export function Select({
  children,
  value,
  onChange,
  disabled,
  required,
  className,
  "aria-label": label,
  "aria-describedby": describedBy,
  ...props
}: SelectProps) {
  const native = useRef<HTMLSelectElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const [invalid, setInvalid] = useState(false);
  return (
    <div
      ref={root}
      className={["form-select", className].filter(Boolean).join(" ")}
    >
      <SelectPicker
        label={label}
        showLabel={false}
        value={value}
        options={optionsFrom(children)}
        describedBy={describedBy}
        disabled={disabled}
        required={required}
        invalid={
          (invalid && required && !value) ||
          props["aria-invalid"] === true ||
          props["aria-invalid"] === "true"
        }
        onChange={(next) => {
          const control = native.current;
          if (!control || control.matches(":disabled")) return;
          control.value = next;
          setInvalid(false);
          control.dispatchEvent(new Event("change", { bubbles: true }));
        }}
      />
      <select
        {...props}
        ref={native}
        className="form-select-native"
        aria-hidden="true"
        tabIndex={-1}
        disabled={disabled}
        required={required}
        value={value}
        onChange={onChange}
        onInvalid={(event) => {
          event.preventDefault();
          setInvalid(true);
          root.current
            ?.querySelector<HTMLButtonElement>("button[role='combobox']")
            ?.focus();
          props.onInvalid?.(event);
        }}
      >
        {children}
      </select>
    </div>
  );
}
