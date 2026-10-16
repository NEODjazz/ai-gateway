import { Link } from "react-router-dom";
import { formatCost, formatTimestamp } from "../../format";
import type { IdentitySummary } from "./useIdentitySummaries";

export type SummaryField = "organization" | "models" | "keys" | "spend" | "budgets";
export function IdentitySummaryCell({ field, summary, kind, id, admin, loading }: { field: SummaryField; summary?: IdentitySummary; kind: "teams" | "users"; id: string; admin: boolean; loading: boolean }) {
  if (!admin) return field === "organization" || field === "models" ? <Link to={`/${kind}/${encodeURIComponent(id)}?tab=${field === "models" ? "policy" : "organization"}`}>View workspace</Link> : <span>Restricted</span>;
  const missing = loading && !summary ? "Loading…" : "Unavailable";
  if (field === "organization") return summary?.directory ? <span title={summary.directory.organization?.id}>{summary.directory.organization?.name || "Unassigned"}</span> : <span>{missing}</span>;
  if (field === "models") return summary?.policy ? <Link to={`/${kind}/${encodeURIComponent(id)}?tab=policy`}>{summary.policy.model_access === "inherit" ? "No additional restriction" : summary.policy.model_access === "deny" ? "Blocked" : `${summary.policy.allowed_models.join(", ")}${summary.policy.model_count > 2 ? ` +${summary.policy.model_count - 2}` : ""}`}<small className="directory-summary-detail">{summary.policy.rpm.toLocaleString()} RPM · {summary.policy.tpm.toLocaleString()} TPM</small></Link> : <span>{missing}</span>;
  if (field === "keys") return summary?.directory ? <Link to={`/${kind}/${encodeURIComponent(id)}?tab=keys`}>{summary.directory.key_count.toLocaleString()}</Link> : <span>{missing}</span>;
  if (field === "spend") return summary?.spend ? <>{summary.spend.length ? summary.spend.map((row) => <div key={row.currency}>{formatCost(row.cost, row.currency)}<small className="directory-summary-detail">{row.requests.toLocaleString()} finalized requests · {row.currency}</small></div>) : "No finalized usage"}</> : <span>{missing}</span>;
  if (!summary?.budgets) return <span>{missing}</span>;
  return <div>{summary.budgets.total ? <>{summary.budgets.data.map((row) => <div key={row.policy.id} title={`Resets ${formatTimestamp(row.window_end)}`}><Link to={`/budgets/${row.policy.id}`}>{row.policy.period} · {row.policy.currency} · {row.policy.enabled ? "Enabled" : "Disabled"}</Link><small className="directory-summary-detail">{formatCost(row.used_cost, row.policy.currency)} / {row.policy.max_cost === undefined ? "No cost cap" : formatCost(row.policy.max_cost, row.policy.currency)}</small>{row.policy.max_tokens !== undefined && <small className="directory-summary-detail">{row.used_tokens.toLocaleString()} / {row.policy.max_tokens.toLocaleString()} tokens</small>}</div>)}{summary.budgets.total > 2 && <small>+{summary.budgets.total - 2} more policies</small>}</> : "No direct budget"}<Link className="directory-summary-detail" to={`/${kind}/${encodeURIComponent(id)}?tab=budgets`}>View budgets</Link></div>;
}

export function IdentityResourcesCell({ summary, kind, id, admin, loading, members, teams }: { summary?: IdentitySummary; kind: "teams" | "users"; id: string; admin: boolean; loading: boolean; members?: number; teams?: string[] }) {
  const path=`/${kind}/${encodeURIComponent(id)}`;
  const missing=loading&&!summary?"Loading…":"Unavailable";
  const p=summary?.policy;
  return <div className="directory-resources">
    <Link to={`${path}?tab=members`}>{kind==="teams" ? members===undefined?"Members unavailable":`${members.toLocaleString()} members` : `${(teams||[]).length.toLocaleString()} teams`}</Link>
    {kind==="teams" && (admin ? p?<Link to={`${path}?tab=policy`} title={p.allowed_models.join(", ")}>{p.model_access==="allowlist"?`${p.model_count} model patterns`:p.model_access==="deny"?"Models blocked":"No added model restriction"}</Link>:<span>Models: {missing}</span>:<Link to={`${path}?tab=policy`}>View model policy</Link>)}
    {admin ? summary?.directory?<Link to={`${path}?tab=keys`}>{summary.directory.key_count.toLocaleString()} keys</Link>:<span>Keys: {missing}</span>:<span>Restricted</span>}
  </div>;
}
