import { useEffect, useRef, useState } from "react";
import { ToolbarIconButton } from "./ToolbarIconButton";

export type ColumnChoice = { key: string; label: string; locked?: boolean };

export function ColumnsMenu({ columns, visible, onChange }: { columns: ColumnChoice[]; visible: Set<string>; onChange: (visible: Set<string>) => void }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => { if (!rootRef.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  function toggle(column: ColumnChoice) {
    if (column.locked) return;
    const next = new Set(visible);
    if (next.has(column.key)) next.delete(column.key); else next.add(column.key);
    onChange(next);
  }

  return <div className="columns-menu" ref={rootRef}>
    <ToolbarIconButton icon="columns" label="Columns" className="columns-menu-trigger" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)} />
    {open && <div className="columns-menu-popover" role="menu" aria-label="Table columns" onKeyDown={(event) => { if (event.key === "Escape") { setOpen(false); rootRef.current?.querySelector<HTMLButtonElement>(".columns-menu-trigger")?.focus(); } }}>
      {columns.map((column) => <button type="button" role="menuitemcheckbox" aria-checked={visible.has(column.key)} disabled={column.locked} key={column.key} onClick={() => toggle(column)}><span aria-hidden="true" className="column-check">{visible.has(column.key) ? "✓" : ""}</span><span>{column.label}</span></button>)}
    </div>}
  </div>;
}
