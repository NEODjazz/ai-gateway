import { useCallback, useEffect, useMemo, useState } from "react";
import { APIError } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Column, type Row } from "./DataTable";
import { ErrorState, LoadingState } from "./AsyncState";
import { PageHeader } from "./PageHeader";
import { ResourceForm, type Field } from "./ResourceForm";
import { ActionsMenu, type ActionMenuItem } from "./ActionsMenu";
import { ManagedDataTable } from "./ManagedDataTable";
import { GatewayButton } from "./GatewayButton";
import { ToolbarIconButton } from "./ToolbarIconButton";

export type ResourceConfig = {
  eyebrow: string;
  title: string;
  description: string;
  listPath: string;
  columns: Column[];
  fields?: Field[];
  createMethod?: "POST" | "PUT";
  createPath?: string;
  itemPath?: (id: string) => string;
  deletePath?: (id: string) => string;
  idKey?: string;
  transformPayload?: (value: Row, editing: boolean) => Row;
  operations?: Array<{ label: string; method?: "GET" | "POST"; path: (row: Row) => string; body?: (row: Row) => unknown }>;
  managedTable?: boolean;
  createLabel?: string;
  inspectPath?: (row: Row) => string;
};

function recordsFrom(payload: unknown): Row[] {
  if (Array.isArray(payload)) return payload as Row[];
  if (payload && typeof payload === "object") {
    const data = (payload as { data?: unknown }).data;
    if (Array.isArray(data)) return data as Row[];
  }
  return [];
}

export function ResourcePage({ config, readOnly = false, allowCreate = true }: { config: ResourceConfig; readOnly?: boolean; allowCreate?: boolean }) {
  const { client } = useAuth();
  const [rows, setRows] = useState<Row[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<Row | null | undefined>(undefined);
  const [operationResult, setOperationResult] = useState("");
  const idKey = config.idKey || "id";
  const loadOptions = useCallback((path: string) => client.request(path), [client]);
  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setRows(recordsFrom(await client.request(config.listPath)));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not load records");
    } finally {
      setLoading(false);
    }
  }, [client, config.listPath]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    const refresh = () => void load();
    window.addEventListener("control-plane-conflict", refresh);
    return () => window.removeEventListener("control-plane-conflict", refresh);
  }, [load]);
  const canCreate = !readOnly && allowCreate && Boolean(config.fields && (config.createPath || config.itemPath));
  const actions = useMemo(() => !readOnly && (config.inspectPath || config.fields || config.deletePath || config.operations?.length) ? (row: Row) => {
    const id = String(row[idKey]);
    const items: ActionMenuItem[] = config.inspectPath ? [{ label: "Inspect", href: config.inspectPath(row) }] : [];
    items.push(...(config.operations || []).map((operation) => ({ label: operation.label, onSelect: async () => {
      setError(""); setOperationResult("");
      try {
        const result = await client.request(operation.path(row), { method: operation.method || "POST", body: operation.body?.(row) });
        setOperationResult(result === undefined ? `${operation.label} succeeded` : `${operation.label}: ${JSON.stringify(result)}`);
      } catch (cause) { setError(cause instanceof Error ? cause.message : `${operation.label} failed`); }
    }})));
    if (config.fields) items.push({ label: "Edit", onSelect: () => setEditing(row) });
    if (config.deletePath) items.push({ label: "Delete", tone: "danger", onSelect: async () => {
      if (!window.confirm(`Delete ${id}?`)) return;
      try { await client.request(config.deletePath!(id), { method: "DELETE" }); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete record"); }
    }});
    return <ActionsMenu label={`Actions for ${id}`} items={items} />;
  } : undefined, [client, config.deletePath, config.fields, config.inspectPath, config.operations, idKey, load, readOnly]);
  const createButton = canCreate ? <GatewayButton size="l" onClick={() => setEditing(null)}>{config.createLabel || "Add"}</GatewayButton> : undefined;
  return <><PageHeader eyebrow={config.eyebrow} title={config.title} description={config.description} actions={config.managedTable ? undefined : <><ToolbarIconButton icon="refresh" label="Refresh table" onClick={() => void load()} />{createButton}</>} />{error && <ErrorState message={error} retry={() => void load()} />}{operationResult && <div className="operation-result" role="status">{operationResult}</div>}{loading ? <LoadingState /> : config.managedTable ? <ManagedDataTable rows={rows} columns={config.columns} actions={actions} primaryAction={createButton} onRefresh={load} searchPlaceholder={`Search ${config.title.toLowerCase()}`} /> : <DataTable rows={rows} columns={config.columns} actions={actions} />}{editing !== undefined && config.fields && <ResourceForm title={`${editing ? "Edit" : "Add"} ${config.title}`} fields={config.fields} initial={editing || undefined} loadOptions={loadOptions} onClose={() => setEditing(undefined)} onSubmit={async (value) => {
    const isEdit = Boolean(editing);
    const id = String(value[idKey] || editing?.[idKey] || "");
    const path = isEdit ? config.itemPath?.(String(editing?.[idKey])) : config.createPath || config.itemPath?.(id);
    if (!path) throw new APIError(400, "missing_path", "Resource does not support this operation");
    const payload = { ...value };
    if (isEdit || (!config.createPath && config.itemPath)) delete payload[idKey];
    const result = await client.request<unknown>(path, { method: isEdit ? "PUT" : config.createMethod || "PUT", body: config.transformPayload ? config.transformPayload(payload, isEdit) : payload });
    if (result && typeof result === "object" && "token" in result) setOperationResult(`Issued once: ${JSON.stringify(result)}`);
    setEditing(undefined);
    await load();
  }} />}</>;
}
