"use client";
import type { ComponentProps } from "react";
import { SelectPicker as SharedSelectPicker } from "@web/select-picker";
import { useT } from "@/lib/i18n";
export type { PickerOption } from "@web/select-picker";
export function SelectPicker(props: ComponentProps<typeof SharedSelectPicker>) {
  const tr = useT();
  return (
    <SharedSelectPicker
      {...props}
      placeholder={props.placeholder || tr("Choose an option")}
      emptyLabel={tr("No matching options")}
    />
  );
}
