import { useEffect, useRef, useState } from "react";
import { Ellipsis } from "@gravity-ui/icons";
import { Button, DropdownMenu, Icon, type DropdownMenuItem } from "@gravity-ui/uikit";
import { GravityThemeScope } from "./GravityThemeScope";

export type ActionMenuItem = {
  label: string;
  onSelect?: () => void | Promise<void>;
  href?: string;
  disabled?: boolean;
  tone?: "default" | "danger";
};

export function ActionsMenu({ label, items }: { label: string; items: ActionMenuItem[] }) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const selector = `[role="menu"][aria-label="${CSS.escape(label)}"] [role="menuitem"]`;
    const timer = window.setTimeout(() => {
      document.querySelector<HTMLElement>(selector)?.focus();
    });
    const navigate = (event: KeyboardEvent) => {
      const menuItems = Array.from(document.querySelectorAll<HTMLElement>(selector)).filter((item) => item.getAttribute("aria-disabled") !== "true");
      if (!menuItems.length) return;
      const index = menuItems.indexOf(document.activeElement as HTMLElement);
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        event.stopImmediatePropagation();
        const step = event.key === "ArrowDown" ? 1 : -1;
        menuItems[(index + step + menuItems.length) % menuItems.length]?.focus();
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopImmediatePropagation();
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener("keydown", navigate, true);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener("keydown", navigate, true);
    };
  }, [label, open]);
  const menuItems: DropdownMenuItem[] = items.map((item) => ({
    text: item.label,
    ...(item.href ? { href: item.href } : { action: () => { queueMicrotask(() => setOpen(false)); void item.onSelect?.(); } }),
    disabled: item.disabled,
    extraProps: item.disabled ? { "aria-disabled": true } : undefined,
    theme: item.tone === "danger" ? "danger" : "normal",
  }));

  return <GravityThemeScope className="gravity-action-menu"><DropdownMenu
    items={menuItems}
    menuProps={{ "aria-label": label }}
    onOpenToggle={setOpen}
    open={open}
    popupProps={{ placement: "bottom-end" }}
    renderSwitcher={(props) => <Button
      ref={triggerRef}
      aria-label={label}
      title={label}
      view="flat"
      size="l"
      onClick={props.onClick}
      onKeyDown={(event) => {
        if (!open && event.key === "ArrowDown") {
          event.preventDefault();
          setOpen(true);
          return;
        }
        props.onKeyDown(event);
      }}
    ><Icon data={Ellipsis} size={18} /></Button>}
    size="l"
  /></GravityThemeScope>;
}
