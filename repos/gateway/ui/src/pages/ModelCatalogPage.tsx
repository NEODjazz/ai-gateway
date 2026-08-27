import { FormEvent, useCallback, useEffect, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { DataTable, type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm, type Field } from "../components/ResourceForm";

type Catalog = { version?: string; models?: Row[]; [key: string]: unknown };
type CatalogPage = Catalog & { data?: Row[]; total?: number; limit?: number; offset?: number };
const fields: Field[] = [{ key: "provider", label: "Provider", type: "reference", required: true, reference: { path: "/admin/v1/providers", labelKeys: ["type", "base_url"] } }, { key: "model", label: "Model", required: true }, { key: "capabilities", label: "Capabilities", type: "csv" }, { key: "max_input_tokens", label: "Maximum input tokens", type: "number" }, { key: "max_output_tokens", label: "Maximum output tokens", type: "number" }, { key: "input_cost_per_1m", label: "Input cost / 1M", type: "number" }, { key: "output_cost_per_1m", label: "Output cost / 1M", type: "number" }, { key: "currency", label: "Currency", defaultValue: "USD" }];

function key(row: Row) { return `${String(row.provider)}\u0000${String(row.model)}`; }

export function ModelCatalogPage() {
  const { client } = useAuth();
  const [catalog, setCatalog] = useState<Catalog>();
  const [rows, setRows] = useState<Row[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [filters, setFilters] = useState({ search: "", provider: "", capability: "", sort: "model", order: "asc" });
  const [editing, setEditing] = useState<Row | null | undefined>(undefined);
  const [error, setError] = useState("");
  const loadOptions = useCallback((path: string) => client.request(path), [client]);
  const load = useCallback(async () => {
    setError("");
    try {
      const params = new URLSearchParams({ ...filters, limit: "25", offset: String(offset) });
      const payload = await client.request<CatalogPage>(`/admin/v1/model-catalog?${params}`);
      const pageRows = payload.data || payload.models || [];
      setCatalog({ version: payload.version, unknown_model_policy: payload.unknown_model_policy }); setRows(pageRows); setTotal(payload.total ?? pageRows.length);
    }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load model catalog"); }
  }, [client, filters, offset]);
  useEffect(() => { void load(); }, [load]);
  async function save(entry: Row) {
    const original = editing ? key(editing) : "";
    const normalized = { ...entry, currency: String(entry.currency || "").toUpperCase() };
    const current = await client.request<Catalog>("/admin/v1/model-catalog");
    const models = (current.models || []).filter((candidate) => key(candidate) !== original && key(candidate) !== key(normalized));
    models.push(normalized);
    await client.request<Catalog>("/admin/v1/model-catalog", { method: "PUT", body: { ...current, version: `ui-${Date.now()}`, models } });
    setEditing(undefined); await load();
  }
  async function remove(row: Row) {
    if (!window.confirm(`Delete pricing metadata for ${String(row.provider)}/${String(row.model)}?`)) return;
    try { const current = await client.request<Catalog>("/admin/v1/model-catalog"); const models = (current.models || []).filter((candidate) => key(candidate) !== key(row)); await client.request<Catalog>("/admin/v1/model-catalog", { method: "PUT", body: { ...current, version: `ui-${Date.now()}`, models } }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not update catalog"); }
  }
  function applyFilters(event: FormEvent) { event.preventDefault(); setOffset(0); void load(); }
  return <><PageHeader eyebrow="Catalog" title="Models" description="Canonical provider/model pricing and capability metadata. Deployments remain separate and cannot create duplicate catalog identities." actions={<><button className="secondary" onClick={() => void load()}>Refresh</button><button onClick={() => setEditing(null)}>Add model</button></>} />
    <form className="filter-card model-filter" onSubmit={applyFilters}><label>Search<input aria-label="Search models" value={filters.search} onChange={(event) => setFilters({ ...filters, search: event.target.value })} /></label><label>Provider<input value={filters.provider} onChange={(event) => setFilters({ ...filters, provider: event.target.value })} /></label><label>Capability<input value={filters.capability} onChange={(event) => setFilters({ ...filters, capability: event.target.value })} /></label><label>Sort<select value={filters.sort} onChange={(event) => setFilters({ ...filters, sort: event.target.value })}><option value="model">Model</option><option value="provider">Provider</option><option value="input_cost">Input cost</option><option value="output_cost">Output cost</option></select></label><label>Order<select value={filters.order} onChange={(event) => setFilters({ ...filters, order: event.target.value })}><option value="asc">Ascending</option><option value="desc">Descending</option></select></label><button>Apply</button></form>
    {error && <ErrorState message={error} retry={() => void load()} />}{!catalog ? <LoadingState /> : <><DataTable rows={rows} columns={[{ key: "model", label: "Model" }, { key: "provider", label: "Provider" }, { key: "capabilities", label: "Capabilities" }, { key: "input_cost_per_1m", label: "Input / 1M" }, { key: "output_cost_per_1m", label: "Output / 1M" }, { key: "currency", label: "Currency" }]} actions={(row) => <div className="inline-actions"><button className="text-button" onClick={() => setEditing(row)}>Edit</button><button className="danger-button" onClick={() => void remove(row)}>Delete</button></div>} /><div className="pagination"><span>{total ? `${offset + 1}–${Math.min(offset + 25, total)} of ${total}` : "0 results"}</span><button className="secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 25))}>Previous</button><button className="secondary" disabled={offset + 25 >= total} onClick={() => setOffset(offset + 25)}>Next</button></div></>}{editing !== undefined && <ResourceForm title={`${editing ? "Edit" : "Add"} model metadata`} fields={fields} initial={editing || undefined} loadOptions={loadOptions} onClose={() => setEditing(undefined)} onSubmit={save} />}</>;
}
