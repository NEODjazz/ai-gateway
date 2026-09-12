import { Button } from "@gravity-ui/uikit";
import type { ChangeEventHandler, ReactNode } from "react";
import { GravityThemeScope } from "./GravityThemeScope";

export function GatewayFileButton({ children, label, accept, multiple, onChange }: { children: ReactNode; label: string; accept?: string; multiple?: boolean; onChange: ChangeEventHandler<HTMLInputElement> }) {
  return <label className="gateway-file-button"><GravityThemeScope className="gravity-control-scope"><Button component="span" size="l" view="outlined">{children}</Button></GravityThemeScope><input aria-label={label} type="file" accept={accept} multiple={multiple} onChange={onChange} /></label>;
}
