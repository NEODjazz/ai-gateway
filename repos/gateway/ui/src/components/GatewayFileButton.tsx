import { Button } from "@gravity-ui/uikit";
import type { ChangeEventHandler, ReactNode } from "react";
import { GravityThemeScope } from "./GravityThemeScope";

export function GatewayFileButton({ children, label, accept, onChange }: { children: ReactNode; label: string; accept?: string; onChange: ChangeEventHandler<HTMLInputElement> }) {
  return <label className="gateway-file-button"><GravityThemeScope className="gravity-control-scope"><Button component="span" size="l" view="outlined">{children}</Button></GravityThemeScope><input aria-label={label} type="file" accept={accept} onChange={onChange} /></label>;
}
