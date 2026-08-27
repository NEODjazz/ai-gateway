import { useCallback, useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm, type Field } from "../components/ResourceForm";

type Catalog = { version?: string; models?: Row[]; [key: string]: unknown };
const fields: Field[] = [{ key: "provider", label: "Provider", required: true }, { key: "model", label: "Model", required: true }, { key: "capabilities", label: "Capabilities", type: "csv" }, { key: "max_input_tokens", label: "Maximum input tokens", type: "number" }, { key: "max_output_tokens", label: "Maximum output tokens", type: "number" }, { key: "input_cost_per_1m", label: "Input cost / 1M", type: "number" }, { key: "output_cost_per_1m", label: "Output cost / 1M", type: "number" }, { key: "currency", label: "Currency", defaultValue: "USD" }];

function key(row: Row) { return `${String(row.provider)}\u0000${String(row.model)}`; }

export function ModelCatalogPage() {
  const { client } = useAuth();
  const [catalog, setCatalog] = useState<Catalog>();
  const [editing, setEditing] = useState<Row | null | undefined>(undefined);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setError("");
    try { setCatalog(await client.request<Catalog>("/admin/v1/model-catalog")); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load model catalog"); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  async function save(entry: Row) {
    const original = editing ? key(editing) : "";
    const normalized = { ...entry, currency: String(entry.currency || "").toUpperCase() };
    const models = (catalog?.models || []).filter((candidate) => key(candidate) !== original && key(candidate) !== key(normalized));
    models.push(normalized);
    setCatalog(await client.request<Catalog>("/admin/v1/model-catalog", { method: "PUT", body: { ...catalog, version: `ui-${Date.now()}`, models } }));
    setEditing(undefined);
  }
  async function remove(row: Row) {
    if (!window.confirm(`Delete pricing metadata for ${String(row.provider)}/${String(row.model)}?`)) return;
    const models = (catalog?.models || []).filter((candidate) => key(candidate) !== key(row));
    try { setCatalog(await client.request<Catalog>("/admin/v1/model-catalog", { method: "PUT", body: { ...catalog, version: `ui-${Date.now()}`, models } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not update catalog"); }
  }
  return <><PageHeader eyebrow="Catalog" title="Models" description="Canonical provider/model pricing and capability metadata. Deployments remain separate and cannot create duplicate catalog identities." actions={<><button className="secondary" onClick={() => void load()}>Refresh</button><button onClick={() => setEditing(null)}>Add model</button></>} />{error && <ErrorState message={error} retry={() => void load()} />}{!catalog ? <LoadingState /> : <DataTable rows={catalog.models || []} columns={[{ key: "model", label: "Model" }, { key: "provider", label: "Provider" }, { key: "capabilities", label: "Capabilities" }, { key: "input_cost_per_1m", label: "Input / 1M" }, { key: "output_cost_per_1m", label: "Output / 1M" }, { key: "currency", label: "Currency" }]} actions={(row) => <div className="inline-actions"><button className="text-button" onClick={() => setEditing(row)}>Edit</button><button className="danger-button" onClick={() => void remove(row)}>Delete</button></div>} />}{editing !== undefined && <ResourceForm title={`${editing ? "Edit" : "Add"} model metadata`} fields={fields} initial={editing || undefined} onClose={() => setEditing(undefined)} onSubmit={save} />}</>;
}
