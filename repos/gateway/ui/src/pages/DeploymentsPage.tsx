import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { DataTable, type Row } from "../components/DataTable";
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
  const loadOptions = useCallback((path: string) => client.request(path), [client]);

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const rows = records<Deployment>(await client.request("/admin/v1/model-deployments"));
      setDeployments(rows);
      const checks = await Promise.all(rows.map(async (row) => {
        try { return records<HealthCheck>(await client.request(`/admin/v1/model-deployments/${encodeURIComponent(row.id)}/health?limit=1`))[0]; }
        catch { return undefined; }
      }));
      setLatest(Object.fromEntries(rows.map((row, index) => [row.id, checks[index]])));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load deployments"); }
    finally { setLoading(false); }
  }, [client]);
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
      const results = await Promise.all(ids.map((id) => client.request<HealthCheck>(`/admin/v1/model-deployments/${encodeURIComponent(id)}/test`, { method: "POST", body: {} })));
      setLatest((current) => ({ ...current, ...Object.fromEntries(results.map((check) => [check.deployment_id, check])) }));
      if (detail && ids.includes(detail.id)) await openDetails(detail);
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
  const allSelected = deployments.length > 0 && deployments.every((row) => selected.has(row.id));

  return <><PageHeader eyebrow="Runtime routing" title="Deployments" description="Provider/model endpoints with live health, circuit state and operational controls." actions={<><Link className="button-link" to="/model-onboarding">Onboard models</Link><button className="secondary" onClick={() => void load()}>Refresh</button><button disabled={busy || !deployments.length} onClick={() => void runChecks(selectedIDs.length ? selectedIDs : deployments.map((row) => row.id))}>{busy ? "Checking…" : selectedIDs.length ? `Check selected (${selectedIDs.length})` : "Check all"}</button></>} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading ? <LoadingState /> : !deployments.length ? <div className="state-card">No deployments found.</div> : <div className="table-card"><div className="table-scroll"><table><thead><tr><th><input aria-label="Select all deployments" type="checkbox" checked={allSelected} onChange={(event) => setSelected(event.target.checked ? new Set(deployments.map((row) => row.id)) : new Set())} /></th><th>Deployment</th><th>Provider</th><th>Models</th><th>Upstream</th><th>Runtime</th><th>Last health</th><th>Latency</th><th><span className="sr-only">Actions</span></th></tr></thead><tbody>{deployments.map((row) => { const check = latest[row.id]; return <tr key={row.id}><td><input aria-label={`Select ${row.id}`} type="checkbox" checked={selected.has(row.id)} onChange={(event) => setSelected((current) => { const next = new Set(current); if (event.target.checked) next.add(row.id); else next.delete(row.id); return next; })} /></td><td>{row.id}</td><td>{row.provider_id}</td><td><div className="tag-list">{row.models.map((model) => <span className="tag" key={model}>{model}</span>)}</div></td><td>{row.upstream_model || "—"}</td><td><span className={`status ${row.enabled && row.runtime_state === "available" ? "enabled" : "disabled"}`}>{row.enabled ? row.runtime_state : "paused"}</span></td><td>{check ? <span className={`status ${check.status === "available" ? "enabled" : "disabled"}`}>{check.status}</span> : <span className="muted">Not checked</span>}</td><td>{check ? `${check.latency_ms} ms` : "—"}</td><td className="row-actions"><div className="inline-actions"><button className="text-button" onClick={() => void openDetails(row)}>Details</button><button className="text-button" onClick={() => void runChecks([row.id])}>Check</button><button className="text-button" onClick={() => setEditing(row)}>Edit</button><button className={row.enabled ? "danger-button" : "text-button"} onClick={() => void toggle(row)}>{row.enabled ? "Pause" : "Resume"}</button></div></td></tr>; })}</tbody></table></div></div>}
    {detail && <div className="modal-backdrop" role="presentation"><section className="modal deployment-detail" role="dialog" aria-modal="true" aria-label="Deployment details"><div className="modal-heading"><div><h2>{detail.id}</h2><span className={`status ${detail.enabled ? "enabled" : "disabled"}`}>{detail.enabled ? detail.runtime_state : "paused"}</span></div><button className="icon-button" aria-label="Close" onClick={() => setDetail(undefined)}>×</button></div><dl className="detail-grid"><div><dt>Provider</dt><dd>{detail.provider_id} · {detail.provider_type}</dd></div><div><dt>Credential</dt><dd>{detail.credential_id || "None"}</dd></div><div><dt>Public models</dt><dd>{detail.models.join(", ")}</dd></div><div><dt>Upstream model</dt><dd>{detail.upstream_model || "—"}</dd></div><div><dt>Priority / weight</dt><dd>{detail.priority} / {detail.weight}</dd></div><div><dt>Adaptive EWMA</dt><dd>{Number(detail.latency_ewma_ms || 0).toFixed(1)} ms · {(Number(detail.failure_ewma || 0) * 100).toFixed(1)}% failures</dd></div></dl><div className="modal-actions"><button disabled={busy} onClick={() => void runChecks([detail.id])}>Run health check</button></div><h3>Health history</h3><DataTable rows={history} columns={[{ key: "checked_at", label: "Checked", render: formatTimestamp }, { key: "status", label: "Status" }, { key: "latency_ms", label: "Latency", render: (value) => `${String(value)} ms` }, { key: "failure_class", label: "Failure" }, { key: "http_status", label: "HTTP" }]} /></section></div>}
    {editing && <ResourceForm title={`Edit ${editing.id}`} fields={resourceConfigs.deployments.fields || []} initial={editing} loadOptions={loadOptions} onClose={() => setEditing(undefined)} onSubmit={save} />}
  </>;
}
