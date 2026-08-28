import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

export type ActionMenuItem = {
  label: string;
  onSelect: () => void | Promise<void>;
  disabled?: boolean;
  tone?: "default" | "danger";
};

type MenuPosition = { top?: number; bottom?: number; right: number };

export function ActionsMenu({ label, items }: { label: string; items: ActionMenuItem[] }) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<MenuPosition>({ top: 0, right: 0 });
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  function openMenu() {
    const rect = triggerRef.current?.getBoundingClientRect();
    if (rect) {
      const estimatedHeight = items.length * 38 + 12;
      setPosition(rect.bottom + estimatedHeight > window.innerHeight
        ? { bottom: window.innerHeight - rect.top + 6, right: Math.max(8, window.innerWidth - rect.right) }
        : { top: rect.bottom + 6, right: Math.max(8, window.innerWidth - rect.right) });
    }
    setOpen(true);
  }

  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!triggerRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false);
    };
    const closeOnViewportChange = () => setOpen(false);
    document.addEventListener("mousedown", close);
    window.addEventListener("resize", closeOnViewportChange);
    window.addEventListener("scroll", closeOnViewportChange, true);
    menuRef.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    return () => {
      document.removeEventListener("mousedown", close);
      window.removeEventListener("resize", closeOnViewportChange);
      window.removeEventListener("scroll", closeOnViewportChange, true);
    };
  }, [open]);

  function menuKeyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    const buttons = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") || []);
    const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (event.key === "Escape") { event.preventDefault(); setOpen(false); triggerRef.current?.focus(); }
    else if (event.key === "ArrowDown") { event.preventDefault(); buttons[(index + 1) % buttons.length]?.focus(); }
    else if (event.key === "ArrowUp") { event.preventDefault(); buttons[(index - 1 + buttons.length) % buttons.length]?.focus(); }
    else if (event.key === "Tab") setOpen(false);
  }

  return <div className="actions-menu-trigger"><button ref={triggerRef} type="button" className="actions-menu-button" aria-label={label} aria-haspopup="menu" aria-expanded={open} onClick={() => open ? setOpen(false) : openMenu()} onKeyDown={(event) => { if (!open && (event.key === "ArrowDown" || event.key === "Enter" || event.key === " ")) { event.preventDefault(); openMenu(); } }}>⋯</button>{open && createPortal(<div ref={menuRef} className="actions-menu-popover" role="menu" aria-label={label} style={position} onKeyDown={menuKeyDown}>{items.map((item) => <button type="button" role="menuitem" key={item.label} disabled={item.disabled} className={item.tone === "danger" ? "danger" : ""} onClick={() => { setOpen(false); void item.onSelect(); }}>{item.label}</button>)}</div>, document.body)}</div>;
}
