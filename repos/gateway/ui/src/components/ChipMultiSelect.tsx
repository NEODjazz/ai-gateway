import { useId, useMemo, useState } from "react";

export type ChipOption = { value: string; label: string; description?: string };

export function ChipMultiSelect({ label, options, value, onChange, allowCustom = false }: { label: string; options: ChipOption[]; value: string[]; onChange: (values: string[]) => void; allowCustom?: boolean }) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const labelID = useId();
  const listID = `${labelID}-options`;
  const search = query.trim().toLowerCase();
  const customValue = query.trim();
  const singular = label.endsWith("ies") ? `${label.slice(0, -3)}y`.toLowerCase() : label.endsWith("s") ? label.slice(0, -1).toLowerCase() : label.toLowerCase();
  const available = useMemo(() => options.filter((option) => !value.includes(option.value) && `${option.label} ${option.value} ${option.description || ""}`.toLowerCase().includes(search)), [options, search, value]);
  const canAddCustom = allowCustom && customValue !== "" && !value.includes(customValue) && !options.some((option) => option.value === customValue);

  function select(selected: string) {
    onChange([...value, selected]);
    setQuery("");
    setOpen(true);
  }

  return <div className="key-model-field"><span id={labelID}>{label}</span><div className="model-multi-select" onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false); }}>
    <div className="model-multi-control" onClick={() => setOpen(true)}>{value.map((selected) => <span className="model-chip" key={selected}>{selected}<button type="button" aria-label={`Remove ${singular} ${selected}`} onClick={(event) => { event.stopPropagation(); onChange(value.filter((item) => item !== selected)); }}>×</button></span>)}<input aria-labelledby={labelID} aria-label={label} role="combobox" aria-expanded={open} aria-controls={listID} placeholder={value.length ? `Select another ${singular}…` : `Search and select ${label.toLowerCase()}…`} value={query} onFocus={() => setOpen(true)} onChange={(event) => { setQuery(event.target.value); setOpen(true); }} onKeyDown={(event) => { if (event.key === "Enter" && available[0]) { event.preventDefault(); select(available[0].value); } else if (event.key === "Enter" && canAddCustom) { event.preventDefault(); select(customValue); } else if (event.key === "Backspace" && !query && value.length) onChange(value.slice(0, -1)); else if (event.key === "Escape") setOpen(false); }} /></div>
    {open && <div className="model-multi-options" id={listID} role="listbox" aria-label={`Available ${label.toLowerCase()}`}>{available.map((option) => <button type="button" role="option" aria-selected="false" key={option.value} onMouseDown={(event) => event.preventDefault()} onClick={() => select(option.value)}><strong>{option.label}</strong>{option.label !== option.value && <small>{option.value}</small>}{option.description && <small>{option.description}</small>}</button>)}{canAddCustom && <button type="button" role="option" aria-selected="false" onMouseDown={(event) => event.preventDefault()} onClick={() => select(customValue)}><strong>Add “{customValue}”</strong><small>Custom exact or wildcard grant</small></button>}{!available.length && !canAddCustom && <span>{options.length ? `No more matching ${label.toLowerCase()}` : `No configured ${label.toLowerCase()}`}</span>}</div>}
  </div></div>;
}
