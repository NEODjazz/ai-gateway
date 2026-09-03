import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Xmark } from "@gravity-ui/icons";
import { Checkbox, Icon, TextArea, TextInput } from "@gravity-ui/uikit";
import type { Row } from "./DataTable";
import { GatewayButton } from "./GatewayButton";
import { GravityThemeScope } from "./GravityThemeScope";

export type FieldReference = {
  path?: string;
  collectionKey?: string;
  valueKey?: string;
  labelKeys?: string[];
  filter?: { fieldKey: string; recordKey: string };
  enabledOnly?: boolean;
  staticOptions?: Array<{ value: string; label: string }>;
};

export type Field = {
  key: string;
  label: string;
  type?: "text" | "password" | "number" | "boolean" | "textarea" | "csv" | "json" | "select" | "reference" | "reference-multi";
  required?: boolean;
  placeholder?: string;
  options?: string[];
  reference?: FieldReference;
  referenceBy?: { fieldKey: string; values: Record<string, FieldReference> };
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
  const referenceSelectorValues = fields.map((field) => field.referenceBy ? String(values[field.referenceBy.fieldKey] || "") : "").join("\0");
  const references = useMemo(() => fields.flatMap((field) => {
    const reference = field.reference || field.referenceBy?.values[String(values[field.referenceBy.fieldKey] || "")];
    return reference?.path ? [{ field, reference }] : [];
  }), [fields, referenceSelectorValues]);
  useEffect(() => {
    if (!loadOptions || !references.length) return;
    let active = true;
    setReferenceError("");
    Promise.all(references.map(async ({ field, reference }) => [field.key, recordsFrom(await loadOptions(reference.path!), reference.collectionKey)] as const))
      .then((entries) => { if (active) setReferenceRows(Object.fromEntries(entries)); })
      .catch((cause) => { if (active) setReferenceError(cause instanceof Error ? cause.message : "Could not load available options"); });
    return () => { active = false; };
  }, [loadOptions, references]);
  function optionsFor(field: Field) {
    const reference = field.reference || field.referenceBy?.values[String(values[field.referenceBy.fieldKey] || "")];
    if (!reference) return (field.options || []).map((value) => ({ value, label: value }));
    const valueKey = reference.valueKey || "id";
    const filter = reference.filter;
    const seen = new Set<string>();
    const options = [...(reference.staticOptions || [])];
    for (const option of options) seen.add(option.value);
    options.push(...(referenceRows[field.key] || []).filter((row) => (!reference.enabledOnly || row.enabled !== false) && (!filter || !values[filter.fieldKey] || String(row[filter.recordKey] || "") === String(values[filter.fieldKey]))).flatMap((row) => {
      const value = String(row[valueKey] || "");
      if (!value || seen.has(value)) return [];
      seen.add(value);
      const details = (reference.labelKeys || []).map((key) => String(row[key] || "")).filter((item) => item && item !== value);
      return [{ value, label: details.length ? `${value} — ${details.join(" · ")}` : value }];
    }));
    for (const value of selectedValues(values[field.key])) if (!seen.has(value)) options.push({ value, label: `${value} — unavailable` });
    return options;
  }
  function setFieldValue(key: string, value: string | number | boolean) {
    setValues((current) => {
      const next = { ...current, [key]: value };
      for (const field of fields) if (field.reference?.filter?.fieldKey === key) next[field.key] = "";
      for (const field of fields) if (field.referenceBy?.fieldKey === key) next[field.key] = "";
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
  function renderControl(field: Field, fieldID: string) {
    if (field.type === "boolean") return <GravityThemeScope className="gravity-form-control"><Checkbox id={fieldID} size="l" checked={Boolean(values[field.key])} onUpdate={(checked) => setFieldValue(field.key, checked)} /></GravityThemeScope>;
    if (field.type === "textarea" || field.type === "json") return <GravityThemeScope className="gravity-form-control"><TextArea id={fieldID} size="l" rows={field.type === "json" ? 7 : 3} controlProps={{ required: field.required }} placeholder={field.placeholder} value={String(values[field.key] ?? "")} onUpdate={(value) => setFieldValue(field.key, value)} /></GravityThemeScope>;
    if (field.type === "select" || field.type === "reference") return <select id={fieldID} required={field.required} value={String(values[field.key] ?? "")} onChange={(event) => setFieldValue(field.key, event.target.value)}><option value="">{field.placeholder || "Select configured item"}</option>{optionsFor(field).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select>;
    if (field.type === "reference-multi") return <select id={fieldID} multiple required={field.required} size={Math.min(8, Math.max(3, optionsFor(field).length))} value={selectedValues(values[field.key])} onChange={(event) => setFieldValue(field.key, Array.from(event.target.selectedOptions, (option) => option.value).join(","))}>{optionsFor(field).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select>;
    return <GravityThemeScope className="gravity-form-control"><TextInput id={fieldID} size="l" type={field.type === "password" ? "password" : field.type === "number" ? "number" : "text"} controlProps={{ required: field.required }} readOnly={Boolean(initial && field.readOnlyOnEdit)} placeholder={field.placeholder} value={String(values[field.key] ?? "")} onUpdate={(value) => setFieldValue(field.key, field.type === "number" ? Number(value) : value)} /></GravityThemeScope>;
  }
  return <div className="modal-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="modal" role="dialog" aria-modal="true" aria-label={title}><div className="modal-heading"><h2>{title}</h2><GatewayButton view="flat" size="l" aria-label="Close" title="Close" onClick={onClose}><Icon data={Xmark} size={18} /></GatewayButton></div><form onSubmit={submit}><div className="form-grid">{fields.filter((field) => !field.visibleWhen || String(values[field.visibleWhen.fieldKey]) === field.visibleWhen.equals).map((field) => {
    const fieldID = `resource-field-${field.key}`;
    return <div key={field.key} className={`resource-form-row${field.type === "textarea" || field.type === "json" || field.type === "reference-multi" ? " span-2" : ""}`}><label htmlFor={fieldID}>{field.label}</label>{renderControl(field, fieldID)}</div>;
  })}</div>{referenceError && <p className="form-error" role="alert">Could not load configured items: {referenceError}</p>}{error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><GatewayButton type="button" view="outlined" size="l" onClick={onClose}>Cancel</GatewayButton><GatewayButton type="submit" size="l" disabled={saving}>{saving ? "Saving…" : "Save"}</GatewayButton></div></form></section></div>;
}
