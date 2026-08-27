import { useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";

type ModelGroup = { id: string; deployment_ids: string[]; strategy: string; retry_policy?: Record<string, number>; enabled: boolean };
type Deployment = {
  id: string; provider_id: string; credential_id?: string; upstream_model?: string; models: string[]; capabilities?: string[];
  priority: number; weight: number; guardrail_policy?: string; request_timeout_ms?: number; max_retries?: number;
  cooldown_after_failures?: number; cooldown_seconds?: number; max_parallel_requests?: number; queue_capacity?: number;
  queue_timeout_ms?: number; enabled: boolean;
};

const failureClasses = ["timeout", "unavailable", "rate_limit", "unknown"] as const;

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function deploymentPayload(row: Deployment) {
  return {
    provider_id: row.provider_id, credential_id: row.credential_id || "", upstream_model: row.upstream_model || "",
    models: row.models || [], capabilities: row.capabilities || [], priority: Number(row.priority || 0), weight: Number(row.weight || 1),
    guardrail_policy: row.guardrail_policy || "", request_timeout_ms: Number(row.request_timeout_ms || 0), max_retries: Number(row.max_retries || 0),
    cooldown_after_failures: Number(row.cooldown_after_failures || 0), cooldown_seconds: Number(row.cooldown_seconds || 0),
    max_parallel_requests: Number(row.max_parallel_requests || 0), queue_capacity: Number(row.queue_capacity || 0),
    queue_timeout_ms: Number(row.queue_timeout_ms || 0), enabled: row.enabled
  };
}

export function RouterSettingsPage() {
  const { client } = useAuth();
  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [draft, setDraft] = useState<ModelGroup>();
  const [deploymentDrafts, setDeploymentDrafts] = useState<Record<string, Deployment>>({});
  const [simulation, setSimulation] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const selectGroup = useCallback((id: string, sourceGroups: ModelGroup[], sourceDeployments: Deployment[]) => {
    const group = sourceGroups.find((item) => item.id === id);
    setSelectedID(id); setSimulation(undefined); setNotice("");
    if (!group) { setDraft(undefined); setDeploymentDrafts({}); return; }
    setDraft({ ...group, deployment_ids: [...group.deployment_ids], retry_policy: { ...(group.retry_policy || {}) } });
    setDeploymentDrafts(Object.fromEntries(sourceDeployments.map((item) => [item.id, { ...item }])));
  }, []);

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [groupPayload, deploymentPayloadValue] = await Promise.all([
        client.request("/admin/v1/model-groups"), client.request("/admin/v1/model-deployments")
      ]);
      const nextGroups = records<ModelGroup>(groupPayload);
      const nextDeployments = records<Deployment>(deploymentPayloadValue);
      setGroups(nextGroups); setDeployments(nextDeployments);
      selectGroup(selectedID && nextGroups.some((item) => item.id === selectedID) ? selectedID : nextGroups[0]?.id || "", nextGroups, nextDeployments);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load router settings"); }
    finally { setLoading(false); }
  }, [client, selectGroup, selectedID]);
  useEffect(() => { void load(); }, [load]);

  const selectedDeployments = useMemo(() => (draft?.deployment_ids || []).map((id) => deploymentDrafts[id]).filter(Boolean), [draft, deploymentDrafts]);

  function toggleDeployment(id: string, checked: boolean) {
    if (!draft) return;
    const ids = checked ? [...draft.deployment_ids, id] : draft.deployment_ids.filter((item) => item !== id);
    setDraft({ ...draft, deployment_ids: ids });
  }
  function move(id: string, direction: -1 | 1) {
    if (!draft) return;
    const ids = [...draft.deployment_ids]; const index = ids.indexOf(id); const target = index + direction;
    if (index < 0 || target < 0 || target >= ids.length) return;
    [ids[index], ids[target]] = [ids[target], ids[index]];
    setDraft({ ...draft, deployment_ids: ids });
    setDeploymentDrafts((current) => {
      const next = { ...current };
      ids.forEach((deploymentID, priority) => { next[deploymentID] = { ...next[deploymentID], priority }; });
      return next;
    });
  }
  function updateDeployment(id: string, patch: Partial<Deployment>) {
    setDeploymentDrafts((current) => ({ ...current, [id]: { ...current[id], ...patch } }));
  }
  async function simulate() {
    if (!draft) return;
    setBusy(true); setError("");
    try { setSimulation(await client.request("/admin/v1/routing/simulate", { method: "POST", body: { model: draft.id, capabilities: [] } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Routing simulation failed"); }
    finally { setBusy(false); }
  }
  async function save() {
    if (!draft || !draft.deployment_ids.length) { setError("Select at least one deployment"); return; }
    setBusy(true); setError(""); setNotice("");
    try {
      await Promise.all(selectedDeployments.map((row) => client.request(`/admin/v1/model-deployments/${encodeURIComponent(row.id)}`, { method: "PUT", body: deploymentPayload(row) })));
      await client.request(`/admin/v1/model-groups/${encodeURIComponent(draft.id)}`, { method: "PUT", body: { deployment_ids: draft.deployment_ids, strategy: draft.strategy, retry_policy: draft.retry_policy, enabled: draft.enabled } });
      setNotice("Router settings saved. Deployment priorities are shared anywhere those deployments are reused.");
      setSimulation(await client.request("/admin/v1/routing/simulate", { method: "POST", body: { model: draft.id, capabilities: [] } }));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save router settings"); }
    finally { setBusy(false); }
  }

  return <><PageHeader eyebrow="Traffic management" title="Router settings" description="Manage fallback membership, weighted or adaptive routing, safe retry classes, timeouts and circuit cooldowns." actions={<><button className="secondary" onClick={() => void load()}>Refresh</button><button disabled={busy || !draft} onClick={() => void save()}>{busy ? "Saving…" : "Save and simulate"}</button></>} />
    {error && <ErrorState message={error} retry={() => void load()} />}{notice && <div className="operation-result" role="status">{notice}</div>}
    {loading ? <LoadingState /> : !groups.length ? <div className="state-card">Create a model group before configuring routing.</div> : draft && <>
      <section className="form-card router-summary">
        <div className="form-grid"><label>Model group<select aria-label="Model group" value={selectedID} onChange={(event) => selectGroup(event.target.value, groups, deployments)}>{groups.map((group) => <option key={group.id}>{group.id}</option>)}</select></label>
          <label>Strategy<select value={draft.strategy} onChange={(event) => setDraft({ ...draft, strategy: event.target.value })}><option value="weighted">Weighted</option><option value="adaptive">Adaptive</option></select></label></div>
        <label className="checkbox-line"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Routing group enabled</label>
        <h2>Retry policy by failure class</h2><p className="muted">Only transient failures are configurable. Authentication, invalid requests and content-policy failures are never retried.</p>
        <div className="retry-grid">{failureClasses.map((failure) => <label key={failure}>{failure.replace("_", " ")}<input aria-label={`Retries ${failure}`} type="number" min="0" max="10" value={draft.retry_policy?.[failure] ?? ""} placeholder="Deployment default" onChange={(event) => { const next = { ...(draft.retry_policy || {}) }; if (event.target.value === "") delete next[failure]; else next[failure] = Number(event.target.value); setDraft({ ...draft, retry_policy: next }); }} /></label>)}</div>
      </section>
      <section className="section-block"><h2>Fallback membership and order</h2><p>Moving a deployment also assigns sequential priority. This priority is global for that deployment.</p><div className="deployment-picker">{deployments.map((row) => <label className="checkbox-line" key={row.id}><input type="checkbox" checked={draft.deployment_ids.includes(row.id)} onChange={(event) => toggleDeployment(row.id, event.target.checked)} /> <strong>{row.id}</strong><span className="muted">{row.provider_id} · {row.upstream_model || row.models.join(", ")}</span></label>)}</div></section>
      <section className="section-block table-card"><div className="table-scroll"><table><thead><tr><th>Order</th><th>Deployment</th><th>Priority</th><th>Weight</th><th>Retries</th><th>Timeout, ms</th><th>Failures</th><th>Cooldown, s</th></tr></thead><tbody>{selectedDeployments.map((row, index) => <tr key={row.id}><td><div className="inline-actions"><button aria-label={`Move ${row.id} up`} className="text-button" disabled={index === 0} onClick={() => move(row.id, -1)}>↑</button><button aria-label={`Move ${row.id} down`} className="text-button" disabled={index === selectedDeployments.length - 1} onClick={() => move(row.id, 1)}>↓</button></div></td><td><strong>{row.id}</strong><br/><span className="muted">{row.provider_id}</span></td>{(["priority", "weight", "max_retries", "request_timeout_ms", "cooldown_after_failures", "cooldown_seconds"] as const).map((field) => <td key={field}><input aria-label={`${field} ${row.id}`} type="number" min="0" value={row[field] ?? 0} onChange={(event) => updateDeployment(row.id, { [field]: Number(event.target.value) })} /></td>)}</tr>)}</tbody></table></div></section>
      <section className="section-block split-grid"><div className="notice-card"><h2>Route preview</h2><ol className="route-preview">{selectedDeployments.map((row) => <li key={row.id}><strong>{row.id}</strong><span>priority {row.priority}, weight {row.weight}</span></li>)}</ol><button className="secondary" disabled={busy} onClick={() => void simulate()}>Simulate current saved route</button></div><div className="json-card"><h2>Runtime simulation</h2><pre>{simulation === undefined ? "Save or run a simulation to inspect the effective candidate order." : JSON.stringify(simulation, null, 2)}</pre></div></section>
    </>}
  </>;
}
