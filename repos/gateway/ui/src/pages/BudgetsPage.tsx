import { BudgetForm, scopeTypes, type BudgetDraft, type BudgetPolicy, type BudgetSummary, type TargetOption } from "./budgets/BudgetForm";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatCost, formatTimestamp } from "../format";

type BudgetList = { data?: BudgetPolicy[]; summaries?: Record<string, BudgetSummary> };
const targetSources: Record<string, { path: string; collection?: string; value?: string; labels: string[] }> = {
  organization: { path: "/admin/v1/organizations?limit=500", labels: ["name"] },
  team: { path: "/admin/v1/teams?limit=500", labels: ["name"] },
  user: { path: "/admin/v1/users?limit=500", labels: ["name", "email"] },
  key: { path: "/admin/v1/keys?limit=500", labels: ["alias"] },
  model: { path: "/admin/v1/model-catalog", collection: "models", value: "model", labels: ["provider"] },
  provider: { path: "/admin/v1/providers", labels: ["type", "base_url"] },
  deployment: { path: "/admin/v1/model-deployments", labels: ["provider_id", "upstream_model"] },
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


export function BudgetsPage() {
  const { client } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
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
  useEffect(() => {
    if (searchParams.get("edit") !== "1") return;
    const id = Number(searchParams.get("budget_id"));
    const policy = policies.find((item) => item.id === id);
    if (policy) setEditing(policy);
  }, [policies, searchParams]);
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
  function closeEditor() {
    setEditing(undefined);
    if (!searchParams.has("edit") && !searchParams.has("budget_id")) return;
    const query = new URLSearchParams(searchParams); query.delete("edit"); query.delete("budget_id");
    setSearchParams(query, { replace: true });
  }
  async function save(draft: BudgetDraft) {
    const body: Record<string, unknown> = { scope_type: draft.scope_type, scope_id: draft.scope_id, period: draft.period, currency: draft.currency, enabled: draft.enabled };
    if (draft.max_cost) body.max_cost = Number(draft.max_cost);
    if (draft.max_tokens) body.max_tokens = Number(draft.max_tokens);
    await client.request(editing ? `/admin/v1/budgets/${editing.id}` : "/admin/v1/budgets", { method: editing ? "PUT" : "POST", body });
    closeEditor(); await load();
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
    <ManagedDataTable rows={rows} columns={columns} defaultHidden={["currency", "updated"]} searchPlaceholder="Search budgets by ID or scope" onRefresh={load} primaryAction={<button onClick={() => setEditing(null)}>Create Budget</button>} toolbarExtra={<label className="table-filter-select"><span className="sr-only">Filter budgets by scope</span><select aria-label="Filter budgets by scope" value={scopeFilter} onChange={(event) => setScopeFilter(event.target.value)}><option value="all">All scopes</option>{scopeTypes.map((scope) => <option key={scope} value={scope}>{scope}</option>)}</select></label>} actions={(row) => { const policy = row._policy as BudgetPolicy; return <ActionsMenu label={`Actions for budget ${policy.id}`} items={[{ label: "Inspect", href: `/budgets/${policy.id}` }, { label: "Edit", onSelect: () => setEditing(policy) }, { label: "Disable", onSelect: () => disable(policy), disabled: !policy.enabled, tone: "danger" }]} />; }} />
    {editing !== undefined && <BudgetForm initial={editing || undefined} loadTargets={loadTargets} onClose={closeEditor} onSave={save} />}
  </>;
}
