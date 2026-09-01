import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatCost, formatTimestamp } from "../format";

type BudgetPolicy = Row & { id: number; scope_type: string; scope_id: string; period: string; currency: string; max_cost?: number; max_tokens?: number; enabled: boolean; created_at: string; updated_at: string };
type BudgetSummary = { policy: BudgetPolicy; window_start: string; window_end: string; used_cost: number; remaining_cost?: number; used_tokens: number; remaining_tokens?: number };
type BudgetList = { data?: BudgetPolicy[]; summaries?: Record<string, BudgetSummary> };
type TargetOption = { value: string; label: string };
type BudgetDraft = { scope_type: string; scope_id: string; period: string; currency: string; max_cost: string; max_tokens: string; enabled: boolean };

const scopeTypes = ["global", "organization", "team", "user", "key", "model", "provider", "tag"];
const periods = ["hour", "day", "week", "month"];
const emptyDraft: BudgetDraft = { scope_type: "global", scope_id: "*", period: "month", currency: "USD", max_cost: "", max_tokens: "", enabled: true };
const targetSources: Record<string, { path: string; collection?: string; value?: string; labels: string[] }> = {
  organization: { path: "/admin/v1/organizations?limit=500", labels: ["name"] },
  team: { path: "/admin/v1/teams?limit=500", labels: ["name"] },
  user: { path: "/admin/v1/users?limit=500", labels: ["name", "email"] },
  key: { path: "/admin/v1/keys?limit=500", labels: ["alias"] },
  model: { path: "/admin/v1/model-catalog", collection: "models", value: "model", labels: ["provider"] },
  provider: { path: "/admin/v1/providers", labels: ["type", "base_url"] },
  tag: { path: "/admin/v1/tags", value: "name", labels: ["description"] }
};

function records(payload: unknown, collection = "data"): Row[] {
  if (Array.isArray(payload)) return payload as Row[];
  if (!payload || typeof payload !== "object") return [];
  const value = (payload as Row)[collection];
  return Array.isArray(value) ? value as Row[] : [];
}

function percent(used: number, limit?: number) {
  return limit && limit > 0 ? used / limit * 100 : 0;
}

function usageState(summary?: BudgetSummary) {
  if (!summary) return "Unavailable";
  if (!summary.policy.enabled) return "Disabled";
  const utilization = Math.max(percent(summary.used_cost, summary.policy.max_cost), percent(summary.used_tokens, summary.policy.max_tokens));
  if (utilization >= 100) return "Exhausted";
  if (utilization >= 80) return "At risk";
  return "Healthy";
}

function UsageCell({ value, limit, type, currency }: { value: number; limit?: number; type: "cost" | "tokens"; currency?: string }) {
  const ratio = percent(value, limit);
  const used = type === "cost" ? formatCost(value, currency || "USD") : value.toLocaleString();
  const cap = limit === undefined ? "No cap" : type === "cost" ? formatCost(limit, currency || "USD") : limit.toLocaleString();
  return <div className="budget-usage-cell"><span>{used} / {cap}</span>{limit !== undefined && <div className="budget-progress" aria-label={`${Math.round(ratio)}% used`}><span className={ratio >= 100 ? "exhausted" : ratio >= 80 ? "risk" : ""} style={{ width: `${Math.min(100, ratio)}%` }} /></div>}</div>;
}

function BudgetForm({ initial, loadTargets, onClose, onSave }: { initial?: BudgetPolicy; loadTargets: (scope: string) => Promise<TargetOption[]>; onClose: () => void; onSave: (draft: BudgetDraft) => Promise<void> }) {
  const [draft, setDraft] = useState<BudgetDraft>(initial ? { scope_type: initial.scope_type, scope_id: initial.scope_id, period: initial.period, currency: initial.currency, max_cost: initial.max_cost === undefined ? "" : String(initial.max_cost), max_tokens: initial.max_tokens === undefined ? "" : String(initial.max_tokens), enabled: initial.enabled } : emptyDraft);
  const [targets, setTargets] = useState<TargetOption[]>(draft.scope_type === "global" ? [{ value: "*", label: "All gateway traffic (*)" }] : []);
  const [targetError, setTargetError] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    let active = true; setTargetError("");
    loadTargets(draft.scope_type).then((options) => { if (active) setTargets(options); }).catch((cause) => { if (active) { setTargets([]); setTargetError(cause instanceof Error ? cause.message : "Could not load configured targets"); } });
    return () => { active = false; };
  }, [draft.scope_type, loadTargets]);
  function changeScope(scope_type: string) { setDraft((value) => ({ ...value, scope_type, scope_id: scope_type === "global" ? "*" : "" })); }
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    const maxCost = draft.max_cost.trim() ? Number(draft.max_cost) : undefined;
    const maxTokens = draft.max_tokens.trim() ? Number(draft.max_tokens) : undefined;
    if (!draft.scope_id || (!maxCost && !maxTokens) || draft.currency.trim().length !== 3) { setError("Select a scope, enter at least one positive limit, and use a three-letter currency code."); return; }
    if ((maxCost !== undefined && maxCost <= 0) || (maxTokens !== undefined && (!Number.isInteger(maxTokens) || maxTokens <= 0))) { setError("Limits must be positive; token limits must be whole numbers."); return; }
    setSaving(true);
    try { await onSave({ ...draft, currency: draft.currency.trim().toUpperCase(), max_cost: maxCost === undefined ? "" : String(maxCost), max_tokens: maxTokens === undefined ? "" : String(maxTokens) }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save budget"); }
    finally { setSaving(false); }
  }
  return <div className="modal-backdrop" role="presentation"><form className="modal key-form-modal" role="dialog" aria-modal="true" aria-label={initial ? "Edit budget" : "Create budget"} onSubmit={submit}>
    <div className="modal-heading"><div><h2>{initial ? "Edit" : "Create"} Budget</h2><span className="muted">Limits are enforced atomically within one currency and reset window.</span></div><button type="button" className="icon-button" aria-label="Close budget form" onClick={onClose}>×</button></div>
    <div className="key-form">
      <label><span>Scope type</span><select aria-label="Budget scope type" value={draft.scope_type} onChange={(event) => changeScope(event.target.value)}>{scopeTypes.map((scope) => <option key={scope} value={scope}>{scope}</option>)}</select></label>
      <label><span>Scope</span><select aria-label="Budget scope" required value={draft.scope_id} onChange={(event) => setDraft({ ...draft, scope_id: event.target.value })}><option value="">Select configured item</option>{targets.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
      <label><span>Reset period</span><select aria-label="Budget reset period" value={draft.period} onChange={(event) => setDraft({ ...draft, period: event.target.value })}>{periods.map((period) => <option key={period} value={period}>{period}</option>)}</select></label>
      <label><span>Currency</span><input aria-label="Budget currency" maxLength={3} value={draft.currency} onChange={(event) => setDraft({ ...draft, currency: event.target.value.toUpperCase() })} /></label>
      <label><span>Maximum cost</span><input aria-label="Budget maximum cost" type="number" min="0" step="0.000001" placeholder="Optional" value={draft.max_cost} onChange={(event) => setDraft({ ...draft, max_cost: event.target.value })} /></label>
      <label><span>Maximum tokens</span><input aria-label="Budget maximum tokens" type="number" min="1" step="1" placeholder="Optional" value={draft.max_tokens} onChange={(event) => setDraft({ ...draft, max_tokens: event.target.value })} /></label>
      <label className="checkbox-line"><input aria-label="Budget enabled" type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /> Enabled</label>
    </div>
    {targetError && <p className="form-error" role="alert">Configured targets are unavailable: {targetError}</p>}{error && <p className="form-error" role="alert">{error}</p>}
    <div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Create budget"}</button></div>
  </form></div>;
}

export function BudgetsPage() {
  const { client } = useAuth();
  const [policies, setPolicies] = useState<BudgetPolicy[]>([]);
  const [summaries, setSummaries] = useState<Record<string, BudgetSummary>>({});
  const [editing, setEditing] = useState<BudgetPolicy | null | undefined>(undefined);
  const [scopeFilter, setScopeFilter] = useState("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try { const payload = await client.request<BudgetList>("/admin/v1/budgets?expand=summaries"); setPolicies(payload.data || []); setSummaries(payload.summaries || {}); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load budgets"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  const loadTargets = useCallback(async (scope: string) => {
    if (scope === "global") return [{ value: "*", label: "All gateway traffic (*)" }];
    const source = targetSources[scope];
    if (!source) return [];
    const payload = await client.request(source.path);
    return records(payload, source.collection).filter((row) => row.enabled !== false && row.status !== "disabled").flatMap((row) => {
      const value = String(row[source.value || "id"] || ""); if (!value) return [];
      const detail = source.labels.map((key) => String(row[key] || "")).filter(Boolean).join(" · ");
      return [{ value, label: detail ? `${value} — ${detail}` : value }];
    });
  }, [client]);
  async function save(draft: BudgetDraft) {
    const body: Record<string, unknown> = { scope_type: draft.scope_type, scope_id: draft.scope_id, period: draft.period, currency: draft.currency, enabled: draft.enabled };
    if (draft.max_cost) body.max_cost = Number(draft.max_cost);
    if (draft.max_tokens) body.max_tokens = Number(draft.max_tokens);
    await client.request(editing ? `/admin/v1/budgets/${editing.id}` : "/admin/v1/budgets", { method: editing ? "PUT" : "POST", body });
    setEditing(undefined); await load();
  }
  async function disable(policy: BudgetPolicy) {
    if (!window.confirm(`Disable budget ${policy.id} for ${policy.scope_type} ${policy.scope_id}?`)) return;
    try { await client.request(`/admin/v1/budgets/${policy.id}`, { method: "DELETE" }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not disable budget"); }
  }
  const filtered = scopeFilter === "all" ? policies : policies.filter((policy) => policy.scope_type === scopeFilter);
  const rows = filtered.map((policy) => {
    const summary = summaries[String(policy.id)];
    return { ...policy, scope: `${policy.scope_type} · ${policy.scope_id}`, cost_usage: summary, token_usage: summary, reset_at: summary?.window_end || "", status: usageState(summary), updated: formatTimestamp(policy.updated_at), _policy: policy };
  });
  const states = policies.map((policy) => usageState(summaries[String(policy.id)]));
  const currencies = new Set(policies.map((policy) => policy.currency)).size;
  const columns = useMemo(() => [
    { key: "id", label: "Budget" }, { key: "scope", label: "Scope" }, { key: "period", label: "Reset" },
    { key: "cost_usage", label: "Cost usage", render: (_: unknown, row: Row) => { const summary = row.cost_usage as BudgetSummary | undefined; return <UsageCell value={summary?.used_cost || 0} limit={summary?.policy.max_cost} type="cost" currency={summary?.policy.currency || String(row.currency || "USD")} />; } },
    { key: "token_usage", label: "Token usage", render: (_: unknown, row: Row) => { const summary = row.token_usage as BudgetSummary | undefined; return <UsageCell value={summary?.used_tokens || 0} limit={summary?.policy.max_tokens} type="tokens" />; } },
    { key: "reset_at", label: "Resets at", render: (value: unknown) => formatTimestamp(value) },
    { key: "status", label: "Status", render: (value: unknown) => <span className={`status ${value === "Healthy" ? "enabled" : "disabled"}`}>{String(value)}</span> },
    { key: "currency", label: "Currency" }, { key: "updated", label: "Updated" }
  ], []);
  if (loading && !policies.length) return <LoadingState />;
  return <><PageHeader eyebrow="Financial controls" title="Budgets" description="Live, currency-isolated spend and token controls by identity, route, provider, model, credential, or tag." />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Active policies" value={policies.filter((policy) => policy.enabled).length} /><StatCard label="At risk" value={states.filter((state) => state === "At risk").length} /><StatCard label="Exhausted" value={states.filter((state) => state === "Exhausted").length} /><StatCard label="Currencies" value={currencies} /></div>
    <ManagedDataTable rows={rows} columns={columns} defaultHidden={["currency", "updated"]} searchPlaceholder="Search budgets by ID or scope" onRefresh={load} primaryAction={<button onClick={() => setEditing(null)}>Create Budget</button>} toolbarExtra={<label className="table-filter-select"><span className="sr-only">Filter budgets by scope</span><select aria-label="Filter budgets by scope" value={scopeFilter} onChange={(event) => setScopeFilter(event.target.value)}><option value="all">All scopes</option>{scopeTypes.map((scope) => <option key={scope} value={scope}>{scope}</option>)}</select></label>} actions={(row) => { const policy = row._policy as BudgetPolicy; return <ActionsMenu label={`Actions for budget ${policy.id}`} items={[{ label: "Edit", onSelect: () => setEditing(policy) }, { label: "Disable", onSelect: () => disable(policy), disabled: !policy.enabled, tone: "danger" }]} />; }} />
    {editing !== undefined && <BudgetForm initial={editing || undefined} loadTargets={loadTargets} onClose={() => setEditing(undefined)} onSave={save} />}
  </>;
}
