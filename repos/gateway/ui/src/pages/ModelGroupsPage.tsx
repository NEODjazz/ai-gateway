import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { GatewayButton } from "../components/GatewayButton";
import { ModalCloseButton } from "../components/ModalCloseButton";

type ModelGroup = Row & { id: string; deployment_ids: string[]; strategy: "weighted" | "adaptive"; retry_policy?: Record<string, number>; fallbacks?: Record<string, string[]>; enabled: boolean };
type Deployment = Row & { id: string; provider_id: string; upstream_model?: string; priority: number; weight: number; enabled: boolean; runtime_state?: string };
type HealthCheck = { deployment_id: string; status: string; latency_ms: number; checked_at: string; failure_class?: string };
type GroupDraft = { id: string; deployment_ids: string[]; strategy: "weighted" | "adaptive"; retry_policy: Record<string, number>; fallbacks: Record<string, string[]>; enabled: boolean };

const failureClasses = [
  { key: "timeout", label: "Timeout" },
  { key: "unavailable", label: "Service unavailable" },
  { key: "rate_limit", label: "Rate limit" },
  { key: "unknown", label: "Unclassified provider failures", description: "Provider failures that could not be mapped to a more specific retry class." }
] as const;
const emptyDraft: GroupDraft = { id: "", deployment_ids: [], strategy: "weighted", retry_policy: {}, fallbacks: {}, enabled: true };

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function chunks<T>(values: T[], size: number): T[][] {
  const result: T[][] = [];
  for (let index = 0; index < values.length; index += size) result.push(values.slice(index, index + size));
  return result;
}

function retrySummary(policy?: Record<string, number>) {
  const entries = failureClasses.filter(({ key }) => policy?.[key] !== undefined).map(({ key, label }) => `${label}: ${policy![key]}`);
  return entries.length ? entries.join(" · ") : "Deployment defaults";
}

function fallbackSummary(fallbacks?: Record<string, string[]>) {
  const entries = Object.entries(fallbacks || {}).filter(([, targets]) => targets.length).map(([type, targets]) => `${type.replace("_", " ")}: ${targets.join(" → ")}`);
  return entries.length ? entries.join(" · ") : "No cross-model fallback";
}

function DeploymentSelector({ deployments, value, onChange }: { deployments: Deployment[]; value: string[]; onChange: (value: string[]) => void }) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const available = deployments.filter((deployment) => !value.includes(deployment.id) && `${deployment.id} ${deployment.provider_id} ${deployment.upstream_model || ""}`.toLowerCase().includes(query.trim().toLowerCase()));
  function select(id: string) { onChange([...value, id]); setQuery(""); setOpen(true); }
  return <div className="model-multi-select" onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false); }}>
    <div className="model-multi-control" onClick={() => setOpen(true)}>{value.map((id) => <span className="model-chip" key={id}>{id}<button type="button" aria-label={`Remove deployment ${id}`} onClick={(event) => { event.stopPropagation(); onChange(value.filter((item) => item !== id)); }}>×</button></span>)}<input aria-label="Deployments" role="combobox" aria-expanded={open} aria-controls="group-deployment-options" placeholder={value.length ? "Select another deployment…" : "Search and select deployments…"} value={query} onFocus={() => setOpen(true)} onChange={(event) => { setQuery(event.target.value); setOpen(true); }} onKeyDown={(event) => { if (event.key === "Enter" && available[0]) { event.preventDefault(); select(available[0].id); } else if (event.key === "Backspace" && !query && value.length) onChange(value.slice(0, -1)); else if (event.key === "Escape") setOpen(false); }} /></div>
    {open && <div className="model-multi-options" id="group-deployment-options" role="listbox" aria-label="Available deployments">{available.length ? available.map((deployment) => <button type="button" role="option" aria-selected="false" key={deployment.id} onMouseDown={(event) => event.preventDefault()} onClick={() => select(deployment.id)}><strong>{deployment.id}</strong><span>{deployment.provider_id} · {deployment.upstream_model || "No upstream model"}</span></button>) : <span>{deployments.length ? "No more matching deployments" : "No configured deployments"}</span>}</div>}
  </div>;
}

function GroupForm({ initial, deployments, onClose, onSave }: { initial?: ModelGroup; deployments: Deployment[]; onClose: () => void; onSave: (value: GroupDraft) => Promise<void> }) {
  const [draft, setDraft] = useState<GroupDraft>(initial ? { id: initial.id, deployment_ids: [...initial.deployment_ids], strategy: initial.strategy, retry_policy: { ...(initial.retry_policy || {}) }, fallbacks: Object.fromEntries(Object.entries(initial.fallbacks || {}).map(([type, targets]) => [type, [...targets]])), enabled: initial.enabled } : { ...emptyDraft, deployment_ids: [], retry_policy: {}, fallbacks: {} });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const selected = draft.deployment_ids.map((id) => deployments.find((deployment) => deployment.id === id)).filter((value): value is Deployment => Boolean(value));
  function move(id: string, direction: -1 | 1) {
    const ids = [...draft.deployment_ids]; const index = ids.indexOf(id); const target = index + direction;
    if (index < 0 || target < 0 || target >= ids.length) return;
    [ids[index], ids[target]] = [ids[target], ids[index]]; setDraft({ ...draft, deployment_ids: ids });
  }
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    if (!draft.id.trim()) { setError("Public model is required"); return; }
    if (!draft.deployment_ids.length) { setError("Select at least one deployment"); return; }
    setBusy(true);
    try { await onSave({ ...draft, id: draft.id.trim() }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save model group"); }
    finally { setBusy(false); }
  }
  return <div className="modal-backdrop" role="presentation"><form className="modal model-group-form-modal" role="dialog" aria-modal="true" aria-label={`${initial ? "Edit" : "Create"} model group`} onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} Model Group</h2><span className="muted">A public model routed across managed deployments.</span></div><ModalCloseButton label="Close model group form" onClick={onClose} /></div>
    <div className="model-group-form"><label><span>Public model</span><input aria-label="Public model" value={draft.id} disabled={Boolean(initial)} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label><label><span>Strategy</span><select aria-label="Strategy" value={draft.strategy} onChange={(event) => setDraft({ ...draft, strategy: event.target.value as GroupDraft["strategy"] })}><option value="weighted">Weighted</option><option value="adaptive">Adaptive</option></select></label><div className="model-group-field"><span>Deployments</span><DeploymentSelector deployments={deployments} value={draft.deployment_ids} onChange={(deployment_ids) => setDraft({ ...draft, deployment_ids })} /></div>
      <label className="checkbox-line model-group-enabled"><input aria-label="Model group enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </div>
    {selected.length > 0 && <section className="selected-route"><h3>Membership order and runtime tiers</h3><p className="muted">Membership order is persisted. Runtime fallback tiers come from deployment priority; weight applies inside a tier.</p>{selected.map((deployment, index) => <div className="selected-route-row" key={deployment.id}><span className="route-position">{index + 1}</span><div><strong>{deployment.id}</strong><small>{deployment.provider_id} · {deployment.upstream_model || "No upstream model"}</small></div><span>priority {deployment.priority}</span><span>weight {deployment.weight}</span><button type="button" className="text-button" aria-label={`Move ${deployment.id} up`} disabled={index === 0} onClick={() => move(deployment.id, -1)}>↑</button><button type="button" className="text-button" aria-label={`Move ${deployment.id} down`} disabled={index === selected.length - 1} onClick={() => move(deployment.id, 1)}>↓</button></div>)}</section>}
    <h3>Retry policy by failure class</h3><p className="muted">Leave a value empty to use the deployment default. Permanent request and authorization errors are never retried.</p><div className="retry-grid">{failureClasses.map(({ key, label, ...failureClass }) => <label key={key} title={"description" in failureClass ? failureClass.description : undefined}>{label}<input aria-label={`Retries ${label}`} type="number" min="0" max="10" value={draft.retry_policy[key] ?? ""} placeholder="Default" onChange={(event) => { const retry_policy = { ...draft.retry_policy }; if (event.target.value === "") delete retry_policy[key]; else retry_policy[key] = Number(event.target.value); setDraft({ ...draft, retry_policy }); }} /></label>)}</div>
    {error && <p className="form-error" role="alert">{error}</p>}<div className="modal-actions"><GatewayButton type="button" view="outlined" onClick={onClose}>Cancel</GatewayButton><GatewayButton type="submit" disabled={busy}>{busy ? "Saving…" : initial ? "Save changes" : "Create group"}</GatewayButton></div>
  </form></div>;
}

export function ModelGroupsPage() {
  const { client } = useAuth();
  const navigate = useNavigate();
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [latest, setLatest] = useState<Record<string, HealthCheck>>({});
  const [editing, setEditing] = useState<ModelGroup | null | undefined>(undefined);
  const [detail, setDetail] = useState<ModelGroup>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const loadLatest = useCallback(async (ids: string[]) => {
    const result: HealthCheck[] = [];
    for (const batch of chunks(ids, 200)) result.push(...records<HealthCheck>(await client.request(`/admin/v1/model-deployments/health?ids=${encodeURIComponent(batch.join(","))}`)));
    setLatest(Object.fromEntries(result.map((check) => [check.deployment_id, check])));
  }, [client]);
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [groupPayload, deploymentPayload] = await Promise.all([client.request("/admin/v1/model-groups"), client.request("/admin/v1/model-deployments")]);
      const nextGroups = records<ModelGroup>(groupPayload); const nextDeployments = records<Deployment>(deploymentPayload);
      setGroups(nextGroups); setDeployments(nextDeployments); await loadLatest(nextDeployments.map((deployment) => deployment.id));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load model groups"); }
    finally { setLoading(false); }
  }, [client, loadLatest]);
  useEffect(() => { void load(); }, [load]);

  async function save(draft: GroupDraft) {
    const current = editing;
    await client.request(current ? `/admin/v1/model-groups/${encodeURIComponent(current.id)}` : "/admin/v1/model-groups", { method: current ? "PUT" : "POST", body: draft });
    setEditing(undefined); await load();
  }
  async function remove(group: ModelGroup) {
    if (!window.confirm(`Delete ${group.id}?`)) return;
    try { await client.request(`/admin/v1/model-groups/${encodeURIComponent(group.id)}`, { method: "DELETE" }); if (detail?.id === group.id) setDetail(undefined); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not delete model group"); }
  }
  async function runChecks(group: ModelGroup) {
    setBusy(true); setError("");
    try {
      const results: HealthCheck[] = [];
      for (const deployment_ids of chunks(group.deployment_ids, 50)) {
        const payload = await client.request<{ data?: HealthCheck[]; errors?: Array<{ deployment_id: string }> }>("/admin/v1/model-deployments/health-checks", { method: "POST", body: { deployment_ids } });
        results.push(...(payload.data || [])); if (payload.errors?.length) setError(`${payload.errors.length} health check(s) could not run`);
      }
      setLatest((current) => ({ ...current, ...Object.fromEntries(results.map((check) => [check.deployment_id, check])) }));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Health checks failed"); }
    finally { setBusy(false); }
  }

  const rows = useMemo(() => groups.map((group) => {
    const members = group.deployment_ids.map((id) => deployments.find((deployment) => deployment.id === id)).filter((value): value is Deployment => Boolean(value));
    return { ...group, deployments: `${members.length} · ${members.map((deployment) => deployment.id).join(", ")}`, available: `${members.filter((deployment) => deployment.enabled && latest[deployment.id]?.status === "available").length}/${members.length}`, retry: retrySummary(group.retry_policy), fallback: fallbackSummary(group.fallbacks) };
  }), [deployments, groups, latest]);
  const columns = [
    { key: "id", label: "Public model" }, { key: "deployments", label: "Deployments" }, { key: "strategy", label: "Strategy" },
    { key: "available", label: "Healthy" }, { key: "retry", label: "Retry policy" }, { key: "fallback", label: "Fallback chains" },
    { key: "enabled", label: "Status", render: (value: unknown) => <span className={`status ${value ? "enabled" : "disabled"}`}>{value ? "Enabled" : "Disabled"}</span> }
  ];
  const enabled = groups.filter((group) => group.enabled).length;
  const healthy = groups.filter((group) => group.enabled && group.deployment_ids.some((id) => deployments.find((deployment) => deployment.id === id)?.enabled && latest[id]?.status === "available")).length;
  const detailMembers = detail?.deployment_ids.map((id) => deployments.find((deployment) => deployment.id === id)).filter((value): value is Deployment => Boolean(value)) || [];

  return <><PageHeader eyebrow="Routing" title="Model groups" description="Manage public model aliases, ordered deployment membership, routing strategy, safe retries and live route health." />
    <div className="usage-stats-grid model-group-stats"><StatCard label="Model groups" value={groups.length} /><StatCard label="Enabled" value={enabled} /><StatCard label="Healthy routes" value={healthy} /><StatCard label="Deployments" value={deployments.length} /></div>
    {error && <ErrorState message={error} retry={() => void load()} />}
    {loading ? <LoadingState /> : <ManagedDataTable rows={rows} columns={columns} primaryAction={<GatewayButton size="l" onClick={() => setEditing(null)}>Create Model Group</GatewayButton>} onRefresh={load} searchPlaceholder="Search model groups" defaultHidden={["retry", "fallback"]} actions={(row) => { const group = groups.find((item) => item.id === row.id)!; return <ActionsMenu label={`Actions for ${group.id}`} items={[{ label: "Routing details", onSelect: () => setDetail(group) }, { label: "Configure routing", onSelect: () => navigate(`/router-settings?group=${encodeURIComponent(group.id)}`) }, { label: "Run health checks", onSelect: () => runChecks(group), disabled: busy }, { label: "Edit", onSelect: () => setEditing(group) }, { label: "Delete", tone: "danger", onSelect: () => remove(group) }]} />; }} />}
    {editing !== undefined && <GroupForm initial={editing || undefined} deployments={deployments} onClose={() => setEditing(undefined)} onSave={save} />}
    {detail && <div className="modal-backdrop" role="presentation"><section className="modal model-group-detail" role="dialog" aria-modal="true" aria-label="Model group routing details"><div className="modal-heading"><div><h2>{detail.id}</h2><span className={`status ${detail.enabled ? "enabled" : "disabled"}`}>{detail.enabled ? "Enabled" : "Disabled"}</span></div><ModalCloseButton label="Close routing details" onClick={() => setDetail(undefined)} /></div><dl className="detail-grid"><div><dt>Strategy</dt><dd>{detail.strategy}</dd></div><div><dt>Retry policy</dt><dd>{retrySummary(detail.retry_policy)}</dd></div><div><dt>Fallback chains</dt><dd>{fallbackSummary(detail.fallbacks)}</dd></div></dl><h3>Effective route topology</h3><p className="muted">Rows preserve group membership order. Priority defines fallback tiers and weight distributes traffic inside each tier.</p><div className="table-card"><div className="table-scroll"><table><thead><tr><th>Order</th><th>Deployment</th><th>Provider / upstream</th><th>Priority</th><th>Weight</th><th>Runtime</th><th>Last health</th><th>Latency</th></tr></thead><tbody>{detailMembers.map((deployment, index) => { const health = latest[deployment.id]; return <tr key={deployment.id}><td>{index + 1}</td><td><strong>{deployment.id}</strong></td><td>{deployment.provider_id}<br/><span className="muted">{deployment.upstream_model || "—"}</span></td><td>{deployment.priority}</td><td>{deployment.weight}</td><td><span className={`status ${deployment.enabled && deployment.runtime_state === "available" ? "enabled" : "disabled"}`}>{deployment.enabled ? deployment.runtime_state || "enabled" : "paused"}</span></td><td>{health ? <span className={`status ${health.status === "available" ? "enabled" : "disabled"}`}>{health.status}</span> : <span className="muted">Not checked</span>}</td><td>{health ? `${health.latency_ms} ms` : "—"}</td></tr>; })}</tbody></table></div></div><div className="modal-actions"><GatewayButton view="outlined" onClick={() => { setEditing(detail); setDetail(undefined); }}>Edit group</GatewayButton><GatewayButton view="outlined" onClick={() => navigate(`/router-settings?group=${encodeURIComponent(detail.id)}`)}>Configure routing</GatewayButton><GatewayButton disabled={busy} onClick={() => void runChecks(detail)}>{busy ? "Checking…" : "Run health checks"}</GatewayButton></div></section></div>}
  </>;
}
