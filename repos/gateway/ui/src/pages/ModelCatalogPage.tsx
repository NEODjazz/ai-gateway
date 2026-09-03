import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { type Row } from "../components/DataTable";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm, type Field } from "../components/ResourceForm";
import { ActionsMenu } from "../components/ActionsMenu";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { ToolbarIconButton } from "../components/ToolbarIconButton";
import { GatewayButton } from "../components/GatewayButton";
import { GatewayFileButton } from "../components/GatewayFileButton";

type Catalog = { version?: string; models?: Row[]; [key: string]: unknown };
type CatalogPage = Catalog & { data?: Row[]; total?: number; limit?: number; offset?: number };
const fields: Field[] = [{ key: "provider", label: "Provider", type: "reference", required: true, reference: { path: "/admin/v1/providers", labelKeys: ["type", "base_url"] } }, { key: "model", label: "Model", required: true }, { key: "capabilities", label: "Capabilities", type: "csv" }, { key: "max_input_tokens", label: "Maximum input tokens", type: "number" }, { key: "max_output_tokens", label: "Maximum output tokens", type: "number" }, { key: "input_cost_per_1m", label: "Input cost / 1M", type: "number" }, { key: "output_cost_per_1m", label: "Output cost / 1M", type: "number" }, { key: "currency", label: "Currency", defaultValue: "USD" }];
const tableColumns = [{ key: "model", label: "Model" }, { key: "provider", label: "Provider" }, { key: "capabilities", label: "Capabilities" }, { key: "input_cost_per_1m", label: "Input / 1M" }, { key: "output_cost_per_1m", label: "Output / 1M" }, { key: "currency", label: "Currency" }, { key: "max_input_tokens", label: "Max input tokens" }, { key: "max_output_tokens", label: "Max output tokens" }];
const catalogSortKeys: Record<string, string> = { model: "model", provider: "provider", input_cost_per_1m: "input_cost", output_cost_per_1m: "output_cost" };

function key(row: Row) { return `${String(row.provider)}\u0000${String(row.model)}`; }

function parsePricingImport(raw: string): Row[] {
  const parsed: unknown = JSON.parse(raw);
  const models = Array.isArray(parsed) ? parsed : parsed && typeof parsed === "object" ? (parsed as { models?: unknown }).models : undefined;
  if (!Array.isArray(models) || models.length > 5000) throw new Error("Import must contain a models array with at most 5,000 entries");
  const seen = new Set<string>();
  return models.map((value, index) => {
    if (!value || typeof value !== "object") throw new Error(`Model entry ${index + 1} must be an object`);
    const row = { ...(value as Row) };
    row.provider = String(row.provider || "").trim(); row.model = String(row.model || "").trim();
    if (!row.provider || !row.model) throw new Error(`Model entry ${index + 1} requires provider and model`);
    if (seen.has(key(row))) throw new Error(`Duplicate model ${row.provider}/${row.model}`);
    seen.add(key(row));
    if (row.currency) row.currency = String(row.currency).toUpperCase();
    return row;
  });
}

export function ModelCatalogPage() {
  const { client } = useAuth();
  const [catalog, setCatalog] = useState<Catalog>();
  const [rows, setRows] = useState<Row[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [limit, setLimit] = useState(25);
  const [filters, setFilters] = useState({ search: "", provider: "", capability: "", sort: "model", order: "asc" });
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [editing, setEditing] = useState<Row | null | undefined>(undefined);
  const [error, setError] = useState("");
  const [importData, setImportData] = useState<{ source: string; current: Catalog; incoming: Row[] }>();
  const [importMode, setImportMode] = useState<"merge" | "replace">("merge");
  const [importConfirmed, setImportConfirmed] = useState(false);
  const [importBusy, setImportBusy] = useState(false);
  const loadOptions = useCallback((path: string) => client.request(path), [client]);
  const load = useCallback(async () => {
    setError("");
    try {
      const params = new URLSearchParams({ ...filters, limit: String(limit), offset: String(offset) });
      const payload = await client.request<CatalogPage>(`/admin/v1/model-catalog?${params}`);
      const pageRows = payload.data || payload.models || [];
      setCatalog({ version: payload.version, unknown_model_policy: payload.unknown_model_policy }); setRows(pageRows); setTotal(payload.total ?? pageRows.length);
    }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load model catalog"); }
  }, [client, filters, limit, offset]);
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
  const importPreview = useMemo(() => {
    if (!importData) return undefined;
    const current = new Map((importData.current.models || []).map((row) => [key(row), row]));
    const incoming = new Map(importData.incoming.map((row) => [key(row), row]));
    const added = importData.incoming.filter((row) => !current.has(key(row)));
    const changed = importData.incoming.filter((row) => current.has(key(row)) && JSON.stringify(current.get(key(row))) !== JSON.stringify(row));
    const unchanged = importData.incoming.filter((row) => current.has(key(row)) && JSON.stringify(current.get(key(row))) === JSON.stringify(row));
    const removed = importMode === "replace" ? (importData.current.models || []).filter((row) => !incoming.has(key(row))) : [];
    const target = importMode === "replace" ? importData.incoming : [...(importData.current.models || []).filter((row) => !incoming.has(key(row))), ...importData.incoming];
    return { added, changed, unchanged, removed, target };
  }, [importData, importMode]);
  async function selectImport(file?: File) {
    if (!file) return;
    setError(""); setImportConfirmed(false); setImportMode("merge");
    try {
      if (file.size > 1 << 20) throw new Error("Pricing import is limited to 1 MiB");
      const incoming = parsePricingImport(await file.text());
      const current = await client.request<Catalog>("/admin/v1/model-catalog");
      setImportData({ source: file.name, current, incoming });
    } catch (cause) { setImportData(undefined); setError(cause instanceof Error ? cause.message : "Invalid pricing import"); }
  }
  async function applyImport() {
    if (!importData || !importPreview || !importConfirmed) return;
    setImportBusy(true); setError("");
    try {
      await client.request("/admin/v1/model-catalog", { method: "PUT", body: { ...importData.current, version: `import-${Date.now()}`, models: importPreview.target } });
      setImportData(undefined); setImportConfirmed(false); setOffset(0); await load();
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not import pricing"); }
    finally { setImportBusy(false); }
  }
  function applyFilters(event: FormEvent) { event.preventDefault(); setOffset(0); setFiltersOpen(false); void load(); }
  return <><PageHeader eyebrow="Catalog" title="Models" description="Canonical provider/model pricing and capability metadata. Deployments remain separate and cannot create duplicate catalog identities." />
    {error && <ErrorState message={error} retry={() => void load()} />}{!catalog ? <LoadingState /> : <ManagedDataTable rows={rows.map((row) => ({ ...row, _identity: key(row) }))} columns={tableColumns} rowKey="_identity" defaultHidden={["max_input_tokens", "max_output_tokens"]} primaryAction={<div className="inline-actions"><GatewayButton size="l" onClick={() => setEditing(null)}>Add model</GatewayButton><GatewayFileButton label="Pricing JSON file" accept="application/json,.json" onChange={(event) => void selectImport(event.target.files?.[0])}>Import pricing</GatewayFileButton></div>} toolbarExtra={<ToolbarIconButton icon="filter" label="Filter" active={Boolean(filters.provider || filters.capability)} onClick={() => setFiltersOpen(true)} />} onRefresh={load} searchPlaceholder="Search models" server={{ search: filters.search, onSearchChange: (search) => { setOffset(0); setFilters((current) => ({ ...current, search })); }, total, offset, pageSize: limit, onPageSizeChange: (value) => { setOffset(0); setLimit(value); }, onOffsetChange: setOffset, sort: Object.keys(catalogSortKeys).find((key) => catalogSortKeys[key] === filters.sort) || "model", direction: filters.order as "asc" | "desc", sortableKeys: Object.keys(catalogSortKeys), onSortChange: (sort, order) => { setOffset(0); setFilters((current) => ({ ...current, sort: catalogSortKeys[sort], order })); } }} actions={(row) => <ActionsMenu label={`Actions for ${String(row.provider)}/${String(row.model)}`} items={[{ label: "Edit", onSelect: () => setEditing(row) }, { label: "Delete", tone: "danger", onSelect: () => remove(row) }]} />}/>} {editing !== undefined && <ResourceForm title={`${editing ? "Edit" : "Add"} model metadata`} fields={fields} initial={editing || undefined} loadOptions={loadOptions} onClose={() => setEditing(undefined)} onSubmit={save} />}
    {filtersOpen && <div className="modal-backdrop" role="presentation"><form className="modal compact-modal" role="dialog" aria-modal="true" aria-label="Filter models" onSubmit={applyFilters}><div className="modal-heading"><h2>Filter models</h2><button type="button" className="icon-button" aria-label="Close filters" onClick={() => setFiltersOpen(false)}>×</button></div><div className="form-grid"><label>Provider<input value={filters.provider} onChange={(event) => setFilters({ ...filters, provider: event.target.value })} /></label><label>Capability<input value={filters.capability} onChange={(event) => setFilters({ ...filters, capability: event.target.value })} /></label></div><div className="modal-actions"><button type="button" className="secondary" onClick={() => { setOffset(0); setFilters((current) => ({ ...current, provider: "", capability: "" })); }}>Reset filters</button><button>Apply filters</button></div></form></div>}
    {importData && importPreview && <div className="modal-backdrop" role="presentation"><section className="modal pricing-import" role="dialog" aria-modal="true" aria-label="Pricing import preview"><div className="modal-heading"><div><h2>Import pricing</h2><span className="muted">Source: {importData.source}</span></div><button className="icon-button" aria-label="Close import" onClick={() => setImportData(undefined)}>×</button></div><div className="form-grid"><label>Apply mode<select aria-label="Import mode" value={importMode} onChange={(event) => { setImportMode(event.target.value as "merge" | "replace"); setImportConfirmed(false); }}><option value="merge">Merge — preserve missing entries</option><option value="replace">Replace entire catalog</option></select></label><div className="import-summary"><span><strong>{importPreview.added.length}</strong> added</span><span><strong>{importPreview.changed.length}</strong> changed</span><span><strong>{importPreview.unchanged.length}</strong> unchanged</span><span><strong>{importPreview.removed.length}</strong> removed</span></div></div><div className="table-card import-diff"><table><thead><tr><th>Change</th><th>Provider</th><th>Model</th><th>Input / 1M</th><th>Output / 1M</th><th>Currency</th></tr></thead><tbody>{([...[...importPreview.added].map((row) => ["Added", row] as const), ...[...importPreview.changed].map((row) => ["Changed", row] as const), ...[...importPreview.removed].map((row) => ["Removed", row] as const)]).map(([change, row]) => <tr key={`${change}-${key(row)}`}><td><span className={`status ${change === "Removed" ? "disabled" : "enabled"}`}>{change}</span></td><td>{String(row.provider)}</td><td>{String(row.model)}</td><td>{String(row.input_cost_per_1m ?? "—")}</td><td>{String(row.output_cost_per_1m ?? "—")}</td><td>{String(row.currency || "—")}</td></tr>)}</tbody></table></div><p className="muted">The import contains pricing metadata only. Providers, credentials, deployments and model groups are not modified.</p><label className="checkbox-line"><input aria-label="Confirm pricing import" type="checkbox" checked={importConfirmed} onChange={(event) => setImportConfirmed(event.target.checked)} /> I reviewed the diff and want to update {importPreview.target.length} catalog entries.</label><div className="modal-actions"><button className="secondary" onClick={() => setImportData(undefined)}>Cancel</button><button disabled={!importConfirmed || importBusy} onClick={() => void applyImport()}>{importBusy ? "Importing…" : "Apply import"}</button></div></section></div>}
  </>;
}
