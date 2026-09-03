import { Button, type ButtonButtonProps } from "@gravity-ui/uikit";
import type { ReactNode } from "react";
import { GravityThemeScope } from "./GravityThemeScope";

export function GatewayButton({ children, view = "action", ...props }: Omit<ButtonButtonProps, "children"> & { children: ReactNode }) {
  return <GravityThemeScope className="gravity-control-scope"><Button {...props} view={view}>{children}</Button></GravityThemeScope>;
}
