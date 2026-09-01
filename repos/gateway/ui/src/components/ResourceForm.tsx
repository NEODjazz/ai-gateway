import { useEffect, useMemo, useState, type FormEvent } from "react";
import type { Row } from "./DataTable";

export type FieldReference = {
  path: string;
  collectionKey?: string;
  valueKey?: string;
  labelKeys?: string[];
  filter?: { fieldKey: string; recordKey: string };
  enabledOnly?: boolean;
};

export type Field = {
  key: string;
  label: string;
  type?: "text" | "password" | "number" | "boolean" | "textarea" | "csv" | "json" | "select" | "reference" | "reference-multi";
  required?: boolean;
  placeholder?: string;
  options?: string[];
  reference?: FieldReference;
  defaultValue?: unknown;
  readOnlyOnEdit?: boolean;
  visibleWhen?: { fieldKey: string; equals: string };
  clears?: string[];
};

function inputValue(field: Field, value: unknown): string | number | boolean {
  if (field.type === "boolean") return Boolean(value);
  if ((field.type === "csv" || field.type === "reference-multi") && Array.isArray(value)) return value.join(",");
  if (field.type === "json" && value && typeof value === "object") return JSON.stringify(value, null, 2);
  if (field.type === "number") return typeof value === "number" ? value : Number(value || 0);
  return String(value ?? "");
}

function outputValue(field: Field, value: string | number | boolean): unknown {
  if (field.type === "boolean") return Boolean(value);
  if (field.type === "number") return Number(value);
  if (field.type === "csv" || field.type === "reference-multi") return String(value).split(",").map((item) => item.trim()).filter(Boolean);
  if (field.type === "json") return String(value).trim() ? JSON.parse(String(value)) : {};
  return String(value);
}

function recordsFrom(payload: unknown, collectionKey = "data"): Row[] {
  if (Array.isArray(payload)) return payload as Row[];
  if (!payload || typeof payload !== "object") return [];
  const records = (payload as Row)[collectionKey];
  return Array.isArray(records) ? records as Row[] : [];
}

function selectedValues(value: unknown) {
  return String(value || "").split(",").map((item) => item.trim()).filter(Boolean);
}

export function ResourceForm({ title, fields, initial, loadOptions, onClose, onSubmit }: { title: string; fields: Field[]; initial?: Row; loadOptions?: (path: string) => Promise<unknown>; onClose: () => void; onSubmit: (value: Row) => Promise<void> }) {
  const [values, setValues] = useState<Record<string, string | number | boolean>>({});
  const [referenceRows, setReferenceRows] = useState<Record<string, Row[]>>({});
  const [referenceError, setReferenceError] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    setValues(Object.fromEntries(fields.map((field) => [field.key, inputValue(field, initial?.[field.key] ?? field.defaultValue)])));
  }, [fields, initial]);
  const references = useMemo(() => fields.filter((field) => field.reference), [fields]);
  useEffect(() => {
    if (!loadOptions || !references.length) return;
    let active = true;
    setReferenceError("");
    Promise.all(references.map(async (field) => [field.key, recordsFrom(await loadOptions(field.reference!.path), field.reference!.collectionKey)] as const))
      .then((entries) => { if (active) setReferenceRows(Object.fromEntries(entries)); })
      .catch((cause) => { if (active) setReferenceError(cause instanceof Error ? cause.message : "Could not load available options"); });
    return () => { active = false; };
  }, [loadOptions, references]);
  function optionsFor(field: Field) {
    const reference = field.reference;
    if (!reference) return (field.options || []).map((value) => ({ value, label: value }));
    const valueKey = reference.valueKey || "id";
    const filter = reference.filter;
    const seen = new Set<string>();
    const options = (referenceRows[field.key] || []).filter((row) => (!reference.enabledOnly || row.enabled !== false) && (!filter || !values[filter.fieldKey] || String(row[filter.recordKey] || "") === String(values[filter.fieldKey]))).flatMap((row) => {
      const value = String(row[valueKey] || "");
      if (!value || seen.has(value)) return [];
      seen.add(value);
      const details = (reference.labelKeys || []).map((key) => String(row[key] || "")).filter((item) => item && item !== value);
      return [{ value, label: details.length ? `${value} — ${details.join(" · ")}` : value }];
    });
    for (const value of selectedValues(values[field.key])) if (!seen.has(value)) options.push({ value, label: `${value} — unavailable` });
    return options;
  }
  function setFieldValue(key: string, value: string | number | boolean) {
    setValues((current) => {
      const next = { ...current, [key]: value };
      for (const field of fields) if (field.reference?.filter?.fieldKey === key) next[field.key] = "";
      for (const cleared of fields.find((field) => field.key === key)?.clears || []) next[cleared] = "";
      return next;
    });
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      const payload = Object.fromEntries(fields.map((field) => [field.key, outputValue(field, values[field.key])])) as Row;
      await onSubmit(payload);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the record");
    } finally {
      setSaving(false);
    }
  }
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal" role="dialog" aria-modal="true" aria-label={title}><div className="modal-heading"><h2>{title}</h2><button className="icon-button" aria-label="Close" onClick={onClose}>×</button></div><form onSubmit={submit}><div className="form-grid">{fields.filter((field) => !field.visibleWhen || String(values[field.visibleWhen.fieldKey]) === field.visibleWhen.equals).map((field) => <label key={field.key} className={field.type === "textarea" || field.type === "json" || field.type === "reference-multi" ? "span-2" : ""}>{field.type === "boolean" ? <><input type="checkbox" checked={Boolean(values[field.key])} onChange={(event) => setFieldValue(field.key, event.target.checked)} /> {field.label}</> : <>{field.label}{field.type === "textarea" || field.type === "json" ? <textarea rows={field.type === "json" ? 7 : 3} required={field.required} placeholder={field.placeholder} value={String(values[field.key] ?? "")} onChange={(event) => setFieldValue(field.key, event.target.value)} /> : field.type === "select" || field.type === "reference" ? <select required={field.required} value={String(values[field.key] ?? "")} onChange={(event) => setFieldValue(field.key, event.target.value)}><option value="">{field.placeholder || "Select configured item"}</option>{optionsFor(field).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select> : field.type === "reference-multi" ? <select multiple required={field.required} size={Math.min(8, Math.max(3, optionsFor(field).length))} value={selectedValues(values[field.key])} onChange={(event) => setFieldValue(field.key, Array.from(event.target.selectedOptions, (option) => option.value).join(","))}>{optionsFor(field).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select> : <input type={field.type === "password" ? "password" : field.type === "number" ? "number" : "text"} required={field.required} readOnly={Boolean(initial && field.readOnlyOnEdit)} placeholder={field.placeholder} value={String(values[field.key] ?? "")} onChange={(event) => setFieldValue(field.key, field.type === "number" ? Number(event.target.value) : event.target.value)} />}</>}</label>)}</div>{referenceError && <p className="form-error" role="alert">Could not load configured items: {referenceError}</p>}{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="submit" disabled={saving}>{saving ? "Saving…" : "Save"}</button></div></form></section></div>;
}
