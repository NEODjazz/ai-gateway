import { useEffect, useState, type FormEvent } from "react";
import type { Row } from "./DataTable";

export type Field = {
  key: string;
  label: string;
  type?: "text" | "password" | "number" | "boolean" | "textarea" | "csv" | "json" | "select";
  required?: boolean;
  placeholder?: string;
  options?: string[];
  defaultValue?: unknown;
  readOnlyOnEdit?: boolean;
};

function inputValue(field: Field, value: unknown): string | number | boolean {
  if (field.type === "boolean") return Boolean(value);
  if (field.type === "csv" && Array.isArray(value)) return value.join(", ");
  if (field.type === "json" && value && typeof value === "object") return JSON.stringify(value, null, 2);
  if (field.type === "number") return typeof value === "number" ? value : Number(value || 0);
  return String(value ?? "");
}

function outputValue(field: Field, value: string | number | boolean): unknown {
  if (field.type === "boolean") return Boolean(value);
  if (field.type === "number") return Number(value);
  if (field.type === "csv") return String(value).split(",").map((item) => item.trim()).filter(Boolean);
  if (field.type === "json") return String(value).trim() ? JSON.parse(String(value)) : {};
  return String(value);
}

export function ResourceForm({ title, fields, initial, onClose, onSubmit }: { title: string; fields: Field[]; initial?: Row; onClose: () => void; onSubmit: (value: Row) => Promise<void> }) {
  const [values, setValues] = useState<Record<string, string | number | boolean>>({});
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    setValues(Object.fromEntries(fields.map((field) => [field.key, inputValue(field, initial?.[field.key] ?? field.defaultValue)])));
  }, [fields, initial]);
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
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal" role="dialog" aria-modal="true" aria-label={title}><div className="modal-heading"><h2>{title}</h2><button className="icon-button" aria-label="Close" onClick={onClose}>×</button></div><form onSubmit={submit}><div className="form-grid">{fields.map((field) => <label key={field.key} className={field.type === "textarea" || field.type === "json" ? "span-2" : ""}>{field.type === "boolean" ? <><input type="checkbox" checked={Boolean(values[field.key])} onChange={(event) => setValues((current) => ({ ...current, [field.key]: event.target.checked }))} /> {field.label}</> : <>{field.label}{field.type === "textarea" || field.type === "json" ? <textarea rows={field.type === "json" ? 7 : 3} required={field.required} placeholder={field.placeholder} value={String(values[field.key] ?? "")} onChange={(event) => setValues((current) => ({ ...current, [field.key]: event.target.value }))} /> : field.type === "select" ? <select required={field.required} value={String(values[field.key] ?? "")} onChange={(event) => setValues((current) => ({ ...current, [field.key]: event.target.value }))}>{field.options?.map((option) => <option key={option} value={option}>{option}</option>)}</select> : <input type={field.type === "password" ? "password" : field.type === "number" ? "number" : "text"} required={field.required} readOnly={Boolean(initial && field.readOnlyOnEdit)} placeholder={field.placeholder} value={String(values[field.key] ?? "")} onChange={(event) => setValues((current) => ({ ...current, [field.key]: field.type === "number" ? Number(event.target.value) : event.target.value }))} />}</>}</label>)}</div>{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="submit" disabled={saving}>{saving ? "Saving…" : "Save"}</button></div></form></section></div>;
}
