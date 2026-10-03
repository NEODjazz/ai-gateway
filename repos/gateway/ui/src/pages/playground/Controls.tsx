import { Select, TextArea, TextInput } from "@gravity-ui/uikit";
import type { ComponentProps } from "react";
import { GravityThemeScope } from "../../components/GravityThemeScope";

export function TextControl({ label, ...props }: Omit<ComponentProps<typeof TextInput>, "aria-label"> & { label: string }) {
  return <label>{label}<GravityThemeScope className="gravity-playground-control"><TextInput aria-label={label} size="l" {...props} /></GravityThemeScope></label>;
}

export function AreaControl({ label, ...props }: Omit<ComponentProps<typeof TextArea>, "controlProps"> & { label: string }) {
  return <label>{label}<GravityThemeScope className="gravity-playground-control"><TextArea size="l" controlProps={{ "aria-label": label }} {...props} /></GravityThemeScope></label>;
}

export function SelectControl<Value extends string>({ label, value, options, onUpdate, disabled = false }: {
  label: string; value: Value; options: { value: Value; content: string }[]; onUpdate: (value: Value) => void; disabled?: boolean;
}) {
  return <label>{label}<GravityThemeScope className="gravity-playground-control"><Select aria-label={label} size="l" width="max" disabled={disabled} value={[value]} options={options} onUpdate={([next]) => { if (next) onUpdate(next as Value); }} /></GravityThemeScope></label>;
}
