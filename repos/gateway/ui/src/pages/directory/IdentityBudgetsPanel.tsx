import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../../auth/AuthContext";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { formatCost, formatTimestamp } from "../../format";
import { BudgetForm, type BudgetDraft, type BudgetPolicy, type BudgetSummary } from "../budgets/BudgetForm";
import { isDirectoryPage, useDirectoryResource, type DirectoryPage } from "./useDirectoryResource";

function validSummary(row: BudgetSummary, kind: string, id: string) {
  return row?.policy?.scope_type === kind && row.policy.scope_id === id && Number.isSafeInteger(row.policy.id) && row.policy.id > 0 && /^[A-Z]{3}$/.test(row.policy.currency) && Number.isFinite(row.used_cost) && row.used_cost >= 0 && Number.isSafeInteger(row.used_tokens) && row.used_tokens >= 0 && Number.isFinite(Date.parse(row.window_start)) && Number.isFinite(Date.parse(row.window_end));
}

export function IdentityBudgetsPanel({ kind, id, allowCreate = true }: { kind: "team" | "user"; id: string; allowCreate?: boolean }) {
  const { client, hasCapability } = useAuth();
  const admin = hasCapability("admin");
  const [offset, setOffset] = useState(0);
  const query = new URLSearchParams({ scope_type: kind, scope_id: id, offset: String(offset), limit: "25" });
  const validate = useCallback((value: unknown) => isDirectoryPage(value) && (value as DirectoryPage<BudgetSummary>).limit <= 100 && (value as DirectoryPage<BudgetSummary>).data.every((row) => validSummary(row, kind, id)), [kind, id]);
  const data = useDirectoryResource<DirectoryPage<BudgetSummary>>(admin ? `/admin/v1/budgets/page?${query}` : undefined, `budgets:${kind}:${id}`, validate);
  const activeScope = useRef(data.scope); activeScope.current = data.scope;
  const [editor, setEditor] = useState<{ scope: string; policy?: BudgetPolicy }>();
  const [mutation, setMutation] = useState<{ scope: string; busy?: boolean; error?: string; notice?: string }>();
  const budgetScope = useMemo(() => ({ type: kind, id }), [kind, id]);
  const page = data.value;
  const busy = Boolean(mutation?.scope === data.scope && mutation.busy);
  useEffect(() => { if (!data.loading && page && page.offset > 0 && page.offset >= page.total) setOffset(page.total ? Math.floor((page.total - 1) / 25) * 25 : 0); }, [data.loading, page]);
  async function save(draft: BudgetDraft) {
    if (!editor || editor.scope !== data.scope) return;
    const scope = data.scope;
    const body = { scope_type: kind, scope_id: id, period: draft.period, currency: draft.currency, enabled: draft.enabled, ...(draft.max_cost ? { max_cost: Number(draft.max_cost) } : {}), ...(draft.max_tokens ? { max_tokens: Number(draft.max_tokens) } : {}) };
    await client.request(editor.policy ? `/admin/v1/budgets/${editor.policy.id}` : "/admin/v1/budgets", { method: editor.policy ? "PUT" : "POST", body });
    if (activeScope.current !== scope) return;
    setEditor((current) => current === editor ? undefined : current);
    setMutation({ scope, notice: "Budget saved. The billing service enforces this policy for matching requests." });
    await data.reload();
  }
  async function disable(policy: BudgetPolicy) {
    if (busy || !window.confirm(`Disable budget ${policy.id} for ${kind} ${id}? Other applicable limits remain enforced.`)) return;
    const scope = data.scope; setMutation({ scope, busy: true });
    try {
      await client.request(`/admin/v1/budgets/${policy.id}`, { method: "DELETE" });
      if (activeScope.current === scope) { setMutation({ scope, notice: "Budget disabled." }); await data.reload(); }
    } catch (cause) { if (activeScope.current === scope) setMutation({ scope, error: cause instanceof Error ? cause.message : "Could not disable budget" }); }
    finally { if (activeScope.current === scope) setMutation((value) => ({ ...value, scope, busy: false })); }
  }
  return <section className="section-block" aria-label="Identity budgets"><h2>Budgets</h2>{!admin ? <p>Financial controls are restricted to global administrators.</p> : <>
    <p>Policies assigned directly to this {kind}. Usage includes committed amounts and live reservations in each policy's currency and reset window. Global, organization, key and other applicable caps remain independent; these rows do not represent all effective limits.</p>
    <div className="inline-actions"><button disabled={!allowCreate || data.loading || busy} onClick={() => setEditor({ scope: data.scope })}>Create budget</button><button className="secondary" onClick={() => void data.reload()}>Refresh budgets</button></div>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}
    {mutation?.scope === data.scope && mutation.error && <ErrorState message={mutation.error} />}
    {mutation?.scope === data.scope && mutation.notice && <p role="status">{mutation.notice}</p>}
    {data.loading && !page ? <LoadingState /> : page && <div aria-busy={data.loading}>
      {data.loading && <p role="status">Refreshing budgets…</p>}{data.previous && data.error && <p>Showing the previous successful page.</p>}
      {page.data.length ? <div className="table-scroll"><table aria-label="Identity budget policies"><thead><tr><th>Budget</th><th>Period</th><th>Cost used / cap</th><th>Tokens used / cap</th><th>Remaining</th><th>Resets at</th><th>Status</th><th>Actions</th></tr></thead><tbody>{page.data.map((summary) => {
        const policy = summary.policy;
        return <tr key={policy.id}><td><Link to={`/budgets/${policy.id}`}>#{policy.id}</Link></td><td>{policy.period} · {policy.currency}</td><td>{formatCost(summary.used_cost, policy.currency)} / {policy.max_cost === undefined ? "No cost cap" : formatCost(policy.max_cost, policy.currency)}</td><td>{summary.used_tokens.toLocaleString()} / {policy.max_tokens === undefined ? "No token cap" : policy.max_tokens.toLocaleString()}</td><td>{policy.max_cost !== undefined ? summary.remaining_cost === undefined ? "Cost unavailable" : formatCost(summary.remaining_cost, policy.currency) : "No cost cap"}<br />{policy.max_tokens !== undefined ? summary.remaining_tokens === undefined ? "Tokens unavailable" : `${summary.remaining_tokens.toLocaleString()} tokens` : "No token cap"}</td><td>{formatTimestamp(summary.window_end)}</td><td>{policy.enabled ? "Enabled" : "Disabled"}</td><td><div className="inline-actions"><button className="secondary" disabled={data.loading || busy} onClick={() => setEditor({ scope: data.scope, policy })}>Edit budget {policy.id}</button><button className="secondary" disabled={!policy.enabled || data.loading || busy} onClick={() => void disable(policy)}>Disable budget {policy.id}</button></div></td></tr>;
      })}</tbody></table></div> : <p>No budgets are assigned directly to this {kind}.</p>}
      <div className="key-pagination"><span>{page.total ? `${page.offset + 1}–${page.offset + page.data.length} of ${page.total}` : "0 budgets"}</span><div className="page-actions"><button className="secondary" disabled={page.offset === 0 || data.loading} onClick={() => setOffset(Math.max(0, page.offset - 25))}>Previous budgets</button><button className="secondary" disabled={page.offset + page.limit >= page.total || data.loading} onClick={() => setOffset(page.offset + page.limit)}>Next budgets</button></div></div>
    </div>}
    {editor?.scope === data.scope && (editor.policy || allowCreate) && <BudgetForm initial={editor.policy} scope={budgetScope} onClose={() => setEditor((current) => current === editor ? undefined : current)} onSave={save} />}
  </>}</section>;
}
