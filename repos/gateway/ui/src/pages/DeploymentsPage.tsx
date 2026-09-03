import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ActionsMenu } from "../components/ActionsMenu";
import { DataTable, type Row } from "../components/DataTable";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { ToolbarIconButton } from "../components/ToolbarIconButton";
import { GatewayButton } from "../components/GatewayButton";
import { ModalCloseButton } from "../components/ModalCloseButton";
import { PageHeader } from "../components/PageHeader";
import { ResourceForm } from "../components/ResourceForm";
import { resourceConfigs } from "./resourceConfigs";
import { formatTimestamp } from "../format";

type Deployment = Row & {
  id: string;
  provider_id: string;
  credential_id?: string;
  provider_type: string;
  upstream_model?: string;
  models: string[];
  capabilities?: string[];
  priority: number;
  weight: number;
  enabled: boolean;
  runtime_state: string;
};
type HealthCheck = Row & { deployment_id: string; status: string; checked_at: string; latency_ms: number; failure_class?: string; http_status?: number };

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function deploymentPayload(row: Deployment, enabled = row.enabled) {
  return {
    provider_id: row.provider_id, credential_id: row.credential_id || "", upstream_model: row.upstream_model || "",
    models: row.models || [], capabilities: row.capabilities || [], priority: Number(row.priority || 0), weight: Number(row.weight || 1),
    guardrail_policy: String(row.guardrail_policy || ""), request_timeout_ms: Number(row.request_timeout_ms || 0),
    max_retries: Number(row.max_retries || 0), cooldown_after_failures: Number(row.cooldown_after_failures || 0),
    cooldown_seconds: Number(row.cooldown_seconds || 0), max_parallel_requests: Number(row.max_parallel_requests || 0),
    queue_capacity: Number(row.queue_capacity || 0), queue_timeout_ms: Number(row.queue_timeout_ms || 0), enabled
  };
}

export function DeploymentsPage() {
  const { client } = useAuth();
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [latest, setLatest] = useState<Record<string, HealthCheck | undefined>>({});
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [detail, setDetail] = useState<Deployment>();
  const [history, setHistory] = useState<HealthCheck[]>([]);
  const [editing, setEditing] = useState<Deployment>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [providerFilter, setProviderFilter] = useState("");
  const [stateFilter, setStateFilter] = useState("");
  const [sort, setSort] = useState("priority");
  const [order, setOrder] = useState("asc");
  const [offset, setOffset] = useState(0);
  const [limit, setLimit] = useState(25);
  const [total, setTotal] = useState(0);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const loadOptions = useCallback((path: string) => client.request(path), [client]);

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const params = new URLSearchParams({ search, provider: providerFilter, state: stateFilter, sort, order, limit: String(limit), offset: String(offset) });
      const payload = await client.request<{ data: Deployment[]; total?: number }>(`/admin/v1/model-deployments?${params}`);
      const rows = records<Deployment>(payload); setTotal(payload.total ?? rows.length);
      setDeployments(rows);
      const checks = rows.length ? records<HealthCheck>(await client.request(`/admin/v1/model-deployments/health?ids=${encodeURIComponent(rows.map((row) => row.id).join(","))}`)) : [];
      setLatest(Object.fromEntries(checks.map((check) => [check.deployment_id, check])));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load deployments"); }
    finally { setLoading(false); }
  }, [client, limit, offset, order, providerFilter, search, sort, stateFilter]);
  useEffect(() => { void load(); }, [load]);

  async function openDetails(row: Deployment) {
    setDetail(row); setHistory([]); setError("");
    try { setHistory(records<HealthCheck>(await client.request(`/admin/v1/model-deployments/${encodeURIComponent(row.id)}/health?limit=50`))); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load deployment health"); }
  }
  async function runChecks(ids: string[]) {
    if (!ids.length) return;
    setBusy(true); setError("");
    try {
      const payload = await client.request<{ data?: HealthCheck[]; errors?: Array<{ deployment_id: string; error: string }> }>("/admin/v1/model-deployments/health-checks", { method: "POST", body: { deployment_ids: ids } });
      const results = payload.data || [];
      setLatest((current) => ({ ...current, ...Object.fromEntries(results.map((check) => [check.deployment_id, check])) }));
      if (detail && ids.includes(detail.id)) await openDetails(detail);
      if (payload.errors?.length) setError(`${payload.errors.length} health check(s) could not run`);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Health check failed"); }
    finally { setBusy(false); }
  }
  async function toggle(row: Deployment) {
    setBusy(true); setError("");
    try { await client.request(`/admin/v1/model-deployments/${encodeURIComponent(row.id)}`, { method: "PUT", body: deploymentPayload(row, !row.enabled) }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not update deployment"); }
    finally { setBusy(false); }
  }
  async function save(value: Row) {
    if (!editing) return;
    await client.request(`/admin/v1/model-deployments/${encodeURIComponent(editing.id)}`, { method: "PUT", body: value });
    setEditing(undefined); await load();
  }
  const selectedIDs = useMemo(() => [...selected], [selected]);
  const tableRows = useMemo(() => deployments.map((row) => ({ ...row, last_health: latest[row.id]?.status, health_latency_ms: latest[row.id]?.latency_ms })), [deployments, latest]);
  const tableColumns = useMemo(() => [
    { key: "id", label: "Deployment" }, { key: "provider_id", label: "Provider" }, { key: "models", label: "Models" }, { key: "upstream_model", label: "Upstream" },
    { key: "runtime_state", label: "Runtime", render: (_: unknown, value: Row) => <span className={`status ${value.enabled && value.runtime_state === "available" ? "enabled" : "disabled"}`}>{value.enabled ? String(value.runtime_state) : "paused"}</span> },
    { key: "last_health", label: "Last health", render: (value: unknown) => value ? <span className={`status ${value === "available" ? "enabled" : "disabled"}`}>{String(value)}</span> : <span className="muted">Not checked</span> },
    { key: "health_latency_ms", label: "Latency", render: (value: unknown) => value === undefined ? "—" : `${String(value)} ms` },
    { key: "priority", label: "Priority" }, { key: "weight", label: "Weight" }
  ], []);
  const deploymentSortKeys: Record<string, string> = { id: "id", provider_id: "provider", priority: "priority", weight: "weight", runtime_state: "state" };

  return <><PageHeader eyebrow="Runtime routing" title="Deployments" description="Provider/model endpoints with live health, circuit state and operational controls." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading ? <LoadingState /> : <ManagedDataTable rows={tableRows} columns={tableColumns} rowKey="id" defaultHidden={["priority", "weight"]} selection={{ selectedIds: selectedIDs, onSelectionChange: (ids) => setSelected(new Set(ids)) }} primaryAction={<div className="inline-actions"><Link className="button-link" to="/model-onboarding">Onboard models</Link><GatewayButton size="l" disabled={busy || !deployments.length} onClick={() => void runChecks(selectedIDs.length ? selectedIDs : deployments.map((row) => row.id))}>{busy ? "Checking…" : selectedIDs.length ? `Check selected (${selectedIDs.length})` : "Check page"}</GatewayButton></div>} toolbarExtra={<ToolbarIconButton icon="filter" label="Filter" active={Boolean(providerFilter || stateFilter)} onClick={() => setFiltersOpen(true)} />} onRefresh={load} searchPlaceholder="Search deployments" server={{ search, onSearchChange: (value) => { setOffset(0); setSearch(value); }, total, offset, pageSize: limit, onPageSizeChange: (value) => { setOffset(0); setLimit(value); }, onOffsetChange: setOffset, sort: Object.keys(deploymentSortKeys).find((key) => deploymentSortKeys[key] === sort) || "priority", direction: order as "asc" | "desc", sortableKeys: Object.keys(deploymentSortKeys), onSortChange: (key, direction) => { setOffset(0); setSort(deploymentSortKeys[key]); setOrder(direction); } }} actions={(value) => { const row = value as Deployment; return <ActionsMenu label={`Actions for ${row.id}`} items={[{ label: "Details", onSelect: () => openDetails(row) }, { label: "Check", onSelect: () => runChecks([row.id]) }, { label: "Edit", onSelect: () => setEditing(row) }, { label: row.enabled ? "Pause" : "Resume", tone: row.enabled ? "danger" : "default", onSelect: () => toggle(row) }]} />; }} />}
    {filtersOpen && <div className="modal-backdrop" role="presentation"><form className="modal compact-modal" role="dialog" aria-modal="true" aria-label="Filter deployments" onSubmit={(event) => { event.preventDefault(); setOffset(0); setFiltersOpen(false); void load(); }}><div className="modal-heading"><h2>Filter deployments</h2><ModalCloseButton label="Close filters" onClick={() => setFiltersOpen(false)} /></div><div className="form-grid"><label>Provider<input value={providerFilter} onChange={(event) => setProviderFilter(event.target.value)} /></label><label>Runtime<select value={stateFilter} onChange={(event) => setStateFilter(event.target.value)}><option value="">All</option><option value="available">Available</option><option value="cooling_down">Cooling down</option><option value="disabled">Disabled</option></select></label></div><div className="modal-actions"><GatewayButton type="button" view="outlined" onClick={() => { setOffset(0); setProviderFilter(""); setStateFilter(""); }}>Reset filters</GatewayButton><GatewayButton type="submit">Apply filters</GatewayButton></div></form></div>}
    {detail && <div className="modal-backdrop" role="presentation"><section className="modal deployment-detail" role="dialog" aria-modal="true" aria-label="Deployment details"><div className="modal-heading"><div><h2>{detail.id}</h2><span className={`status ${detail.enabled ? "enabled" : "disabled"}`}>{detail.enabled ? detail.runtime_state : "paused"}</span></div><ModalCloseButton label="Close" onClick={() => setDetail(undefined)} /></div><dl className="detail-grid"><div><dt>Provider</dt><dd>{detail.provider_id} · {detail.provider_type}</dd></div><div><dt>Credential</dt><dd>{detail.credential_id || "None"}</dd></div><div><dt>Public models</dt><dd>{detail.models.join(", ")}</dd></div><div><dt>Upstream model</dt><dd>{detail.upstream_model || "—"}</dd></div><div><dt>Priority / weight</dt><dd>{detail.priority} / {detail.weight}</dd></div><div><dt>Adaptive EWMA</dt><dd>{Number(detail.latency_ewma_ms || 0).toFixed(1)} ms · {(Number(detail.failure_ewma || 0) * 100).toFixed(1)}% failures</dd></div></dl><div className="modal-actions"><GatewayButton disabled={busy} onClick={() => void runChecks([detail.id])}>Run health check</GatewayButton></div><h3>Health history</h3><DataTable rows={history} columns={[{ key: "checked_at", label: "Checked", render: formatTimestamp }, { key: "status", label: "Status" }, { key: "latency_ms", label: "Latency", render: (value) => `${String(value)} ms` }, { key: "failure_class", label: "Failure" }, { key: "http_status", label: "HTTP" }]} /></section></div>}
    {editing && <ResourceForm title={`Edit ${editing.id}`} fields={resourceConfigs.deployments.fields || []} initial={editing} loadOptions={loadOptions} onClose={() => setEditing(undefined)} onSubmit={save} />}
  </>;
}
