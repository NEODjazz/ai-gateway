import { ArrowRotateRight, Funnel, LayoutColumns } from "@gravity-ui/icons";
import { Button, Icon, type ButtonButtonProps } from "@gravity-ui/uikit";
import { GravityThemeScope } from "./GravityThemeScope";

const icons = {
  columns: LayoutColumns,
  filter: Funnel,
  refresh: ArrowRotateRight,
} as const;

export type ToolbarIcon = keyof typeof icons;

export function ToolbarIconButton({
  icon,
  label,
  active = false,
  className = "",
  ...props
}: Omit<ButtonButtonProps, "children" | "view" | "size"> & {
  icon: ToolbarIcon;
  label: string;
  active?: boolean;
}) {
  return <GravityThemeScope className="gravity-control-scope"><Button
      {...props}
      view="outlined"
      size="l"
      className={`gateway-icon-button${active ? " active" : ""}${className ? ` ${className}` : ""}`}
      aria-label={label}
      title={label}
    >
      <Icon data={icons[icon]} size={16} />
      {active && <span className="gateway-icon-button-indicator" aria-hidden="true" />}
    </Button></GravityThemeScope>;
}
