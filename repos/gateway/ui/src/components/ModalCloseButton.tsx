import { Xmark } from "@gravity-ui/icons";
import { Button, Icon, type ButtonButtonProps } from "@gravity-ui/uikit";
import { GravityThemeScope } from "./GravityThemeScope";

export function ModalCloseButton({ label, ...props }: Omit<ButtonButtonProps, "children" | "view" | "size"> & { label: string }) {
  return <GravityThemeScope className="gravity-control-scope"><Button {...props} view="flat" size="m" className="gateway-modal-close" aria-label={label} title={label}><Icon data={Xmark} size={16} /></Button></GravityThemeScope>;
}
