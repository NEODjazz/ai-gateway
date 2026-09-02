import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { formatCost, formatTimestamp } from "../format";

type BudgetPolicy = { id: number; scope_type: string; scope_id: string; period: string; currency: string; max_cost?: number; max_tokens?: number; enabled: boolean; created_at: string; updated_at: string };
type BudgetSummary = { policy: BudgetPolicy; window_start: string; window_end: string; used_cost: number; remaining_cost?: number; used_tokens: number; remaining_tokens?: number };

function percentage(used: number, limit?: number) {
  return limit && limit > 0 ? used / limit * 100 : 0;
}

function state(summary: BudgetSummary) {
  if (!summary.policy.enabled) return "Disabled";
  const utilization = Math.max(percentage(summary.used_cost, summary.policy.max_cost), percentage(summary.used_tokens, summary.policy.max_tokens));
  if (utilization >= 100) return "Exhausted";
  if (utilization >= 80) return "At risk";
  return "Healthy";
}

function ScopeTarget({ policy }: { policy: BudgetPolicy }) {
  const paths: Record<string, string> = { organization: "/organizations", team: "/teams", key: "/api-keys" };
  const base = paths[policy.scope_type];
  return base ? <Link to={`${base}/${encodeURIComponent(policy.scope_id)}`}>{policy.scope_id}</Link> : <>{policy.scope_id}</>;
}

function UsagePanel({ title, used, limit, remaining, format }: { title: string; used: number; limit?: number; remaining?: number; format: (value: number) => string }) {
  const ratio = percentage(used, limit);
  return <section className="notice-card budget-detail-usage"><div className="modal-heading"><h2>{title}</h2><strong>{format(used)} / {limit === undefined ? "No cap" : format(limit)}</strong></div>{limit !== undefined && <><div className="budget-progress" aria-label={`${Math.round(ratio)}% ${title.toLowerCase()} used`}><span className={ratio >= 100 ? "exhausted" : ratio >= 80 ? "risk" : ""} style={{ width: `${Math.min(100, ratio)}%` }} /></div><p>{format(remaining || 0)} remaining in the current window.</p></>}</section>;
}

export function BudgetDetailsPage() {
  const { id = "" } = useParams();
  const { client } = useAuth();
  const navigate = useNavigate();
  const [policy, setPolicy] = useState<BudgetPolicy>();
  const [summary, setSummary] = useState<BudgetSummary>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [policyPayload, summaryPayload] = await Promise.all([
        client.request<BudgetPolicy>(`/admin/v1/budgets/${encodeURIComponent(id)}`),
        client.request<BudgetSummary>(`/admin/v1/budgets/${encodeURIComponent(id)}/summary`),
      ]);
      setPolicy(policyPayload); setSummary(summaryPayload);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load budget details"); }
    finally { setLoading(false); }
  }, [client, id]);
  useEffect(() => { void load(); }, [load]);
  async function disable() {
    if (!policy || !window.confirm(`Disable budget ${policy.id} for ${policy.scope_type} ${policy.scope_id}?`)) return;
    try { await client.request(`/admin/v1/budgets/${policy.id}`, { method: "DELETE" }); navigate("/budgets"); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not disable budget"); }
  }
  if (loading && !policy) return <LoadingState />;
  if (error && !policy) return <ErrorState message={error} retry={() => void load()} />;
  if (!policy || !summary) return <ErrorState message="Budget was not found" retry={() => void load()} />;
  const usageState = state(summary);
  const actions = [{ label: "Edit budget", onSelect: () => navigate(`/budgets?budget_id=${policy.id}&edit=1`) }, { label: "Disable", onSelect: disable, disabled: !policy.enabled, tone: "danger" as const }];
  return <><PageHeader eyebrow="Financial controls" title={`Budget ${policy.id}`} description="Live enforcement window, limits and scope identity from the billing control plane." actions={<div className="inline-actions"><button className="secondary" onClick={() => navigate("/budgets")}>Back to budgets</button><ActionsMenu label={`Actions for budget ${policy.id}`} items={actions} /></div>} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="State" value={usageState} /><StatCard label="Currency" value={policy.currency} /><StatCard label="Window starts" value={formatTimestamp(summary.window_start)} /><StatCard label="Resets at" value={formatTimestamp(summary.window_end)} /></div>
    <section className="table-card access-group-details"><h2>Policy</h2><dl className="detail-grid"><div><dt>Scope type</dt><dd>{policy.scope_type}</dd></div><div><dt>Scope target</dt><dd><ScopeTarget policy={policy} /></dd></div><div><dt>Reset period</dt><dd>{policy.period}</dd></div><div><dt>Enforcement</dt><dd><span className={`status ${policy.enabled ? "enabled" : "disabled"}`}>{policy.enabled ? "Enabled" : "Disabled"}</span></dd></div><div><dt>Created</dt><dd>{formatTimestamp(policy.created_at)}</dd></div><div><dt>Updated</dt><dd>{formatTimestamp(policy.updated_at)}</dd></div></dl></section>
    <section className="section-block split-grid"><UsagePanel title="Cost" used={summary.used_cost} limit={policy.max_cost} remaining={summary.remaining_cost} format={(value) => formatCost(value, policy.currency)} /><UsagePanel title="Tokens" used={summary.used_tokens} limit={policy.max_tokens} remaining={summary.remaining_tokens} format={(value) => value.toLocaleString()} /></section>
    <section className="notice-card"><h2>Enforcement semantics</h2><p>This policy is evaluated independently from every other matching budget. Currency is never converted or combined, and the billing service owns the authoritative usage and reset window.</p></section>
  </>;
}
