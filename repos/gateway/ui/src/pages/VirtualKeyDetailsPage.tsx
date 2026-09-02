import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import type { Row } from "../components/DataTable";
import { formatCost, formatTimestamp } from "../format";

type VirtualKey = {
  id: string; alias?: string; description?: string; tags?: string[]; user_id?: string; team_id?: string; organization_id?: string;
  roles?: string[]; access_group_ids?: string[]; allowed_models?: string[]; allowed_tools?: string[]; rate_limit_rpm?: number;
  rate_limit_tpm?: number; expires_at?: string; revoked_at?: string; disabled_at?: string; created_at: string;
};
type DirectoryEntry = { id: string; name?: string; email?: string };
type BudgetPolicy = { id: number; scope_type: string; scope_id: string; period: string; currency: string; max_cost?: number; max_tokens?: number; enabled: boolean };
type BudgetSummary = { policy: BudgetPolicy; window_start: string; window_end: string; used_cost: number; remaining_cost?: number; used_tokens: number; remaining_tokens?: number };
type KeyBudgetProjection = { key_id: string; policies: BudgetSummary[] };
type UsageAggregate = { date?: string; currency: string; requests: number; errors: number; input_tokens: number; output_tokens: number; total_tokens: number; cache_hits: number; cost: number; avg_latency_ms: number };
type UsageReport = { totals: UsageAggregate[]; daily: UsageAggregate[] };
type IssuedKey = { id: string; token: string; expires_at?: string };
type Tab = "overview" | "usage" | "settings";

function records<T>(payload: unknown): T[] {
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}

function keyStatus(key: VirtualKey) {
  if (key.revoked_at) return "revoked";
  if (key.disabled_at) return "disabled";
  if (key.expires_at && new Date(key.expires_at) <= new Date()) return "expired";
  return "active";
}

function existingPolicyPayload(key: VirtualKey) {
  const owner = key.user_id ? { user_id: key.user_id } : key.team_id ? { team_id: key.team_id } : { organization_id: key.organization_id || "" };
  return { alias: key.alias || "", description: key.description || "", tags: key.tags || [], ...owner, roles: key.roles || [], access_group_ids: key.access_group_ids || [], allowed_models: key.allowed_models || [], allowed_tools: key.allowed_tools || [], rate_limit_rpm: key.rate_limit_rpm || 0, rate_limit_tpm: key.rate_limit_tpm || 0, expires_at: key.expires_at };
}

function amounts(rows: UsageAggregate[]) {
  const totals = new Map<string, number>();
  for (const row of rows) totals.set(row.currency || "USD", (totals.get(row.currency || "USD") || 0) + row.cost);
  return [...totals].sort(([left], [right]) => left.localeCompare(right)).map(([currency, cost]) => formatCost(cost, currency)).join(" · ") || formatCost(0, "USD");
}

function values(items: string[] | undefined, empty: string) {
  return <div className="tag-list">{items?.map((item) => <span className="tag" key={item}>{item}</span>)}{!items?.length && <span className="muted">{empty}</span>}</div>;
}

export function VirtualKeyDetailsPage() {
  const { id = "" } = useParams();
  const { client } = useAuth();
  const navigate = useNavigate();
  const [key, setKey] = useState<VirtualKey>();
  const [projection, setProjection] = useState<KeyBudgetProjection>();
  const [usage, setUsage] = useState<UsageReport>();
  const [users, setUsers] = useState<DirectoryEntry[]>([]);
  const [teams, setTeams] = useState<DirectoryEntry[]>([]);
  const [organizations, setOrganizations] = useState<DirectoryEntry[]>([]);
  const [accessGroups, setAccessGroups] = useState<DirectoryEntry[]>([]);
  const [tab, setTab] = useState<Tab>("overview");
  const [issued, setIssued] = useState<IssuedKey>();
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [keyPayload, usagePayload, userPayload, teamPayload, organizationPayload, accessGroupPayload] = await Promise.all([
        client.request<{ data?: VirtualKey[]; financials?: Record<string, KeyBudgetProjection> }>(`/admin/v1/keys?key_id=${encodeURIComponent(id)}&limit=10&expand=financials`),
        client.request<UsageReport>(`/admin/v1/usage/report?days=30&scope_type=key&scope_id=${encodeURIComponent(id)}`),
        client.request("/admin/v1/users?limit=500"), client.request("/admin/v1/teams?limit=500"),
        client.request("/admin/v1/organizations?limit=500"), client.request("/admin/v1/access-groups")
      ]);
      setKey(records<VirtualKey>(keyPayload).find((item) => item.id === id));
      setProjection(keyPayload.financials?.[id]); setUsage(usagePayload);
      setUsers(records<DirectoryEntry>(userPayload)); setTeams(records<DirectoryEntry>(teamPayload));
      setOrganizations(records<DirectoryEntry>(organizationPayload)); setAccessGroups(records<DirectoryEntry>(accessGroupPayload));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load virtual key details"); }
    finally { setLoading(false); }
  }, [client, id]);

  useEffect(() => { void load(); }, [load]);
  const directories = useMemo(() => ({
    users: new Map(users.map((item) => [item.id, item])), teams: new Map(teams.map((item) => [item.id, item])),
    organizations: new Map(organizations.map((item) => [item.id, item])), groups: new Map(accessGroups.map((item) => [item.id, item]))
  }), [accessGroups, organizations, teams, users]);
  const status = key ? keyStatus(key) : "unknown";
  const totals = usage?.totals || [];
  const requests = totals.reduce((sum, row) => sum + row.requests, 0);
  const tokens = totals.reduce((sum, row) => sum + row.total_tokens, 0);
  const errors = totals.reduce((sum, row) => sum + row.errors, 0);
  const owner = key?.user_id ? directories.users.get(key.user_id) : key?.team_id ? directories.teams.get(key.team_id) : key?.organization_id ? directories.organizations.get(key.organization_id) : undefined;
  const ownerType = key?.user_id ? "User" : key?.team_id ? "Team" : key?.organization_id ? "Organization" : "Unassigned";
  const ownerID = key?.user_id || key?.team_id || key?.organization_id || "—";
  const budgetRows: Row[] = (projection?.policies || []).map((summary) => ({
    id: summary.policy.id, scope: summary.policy.scope_type === "global" ? "global" : `${summary.policy.scope_type}:${summary.policy.scope_id}`,
    period: summary.policy.period, currency: summary.policy.currency,
    limit: [summary.policy.max_cost !== undefined && formatCost(summary.policy.max_cost, summary.policy.currency), summary.policy.max_tokens !== undefined && `${summary.policy.max_tokens.toLocaleString()} tokens`].filter(Boolean).join(" · ") || "—",
    used: [summary.policy.max_cost !== undefined && formatCost(summary.used_cost, summary.policy.currency), summary.policy.max_tokens !== undefined && `${summary.used_tokens.toLocaleString()} tokens`].filter(Boolean).join(" · ") || "—",
    remaining: [summary.policy.max_cost !== undefined && formatCost(summary.remaining_cost || 0, summary.policy.currency), summary.policy.max_tokens !== undefined && `${Number(summary.remaining_tokens || 0).toLocaleString()} tokens`].filter(Boolean).join(" · ") || "—",
    reset: formatTimestamp(summary.window_end), status: summary.policy.enabled ? "enabled" : "disabled"
  }));
  const dailyRows: Row[] = (usage?.daily || []).map((row, index) => ({ id: `${row.date}-${row.currency}-${index}`, date: row.date || "—", requests: row.requests, errors: row.errors, tokens: row.total_tokens, spend: formatCost(row.cost, row.currency), latency: `${Math.round(row.avg_latency_ms)} ms` }));

  async function rotate() {
    if (!key) return;
    try { const result = await client.request<IssuedKey>(`/admin/v1/keys/${encodeURIComponent(key.id)}/rotate`, { method: "POST", body: existingPolicyPayload(key) }); setIssued(result); setCopied(false); setCopyError(""); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not rotate virtual key"); }
  }
  async function toggle() {
    if (!key) return;
    try { await client.request(`/admin/v1/keys/${encodeURIComponent(key.id)}/${key.disabled_at ? "enable" : "disable"}`, { method: "POST", body: {} }); await load(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not change virtual key status"); }
  }
  async function revoke() {
    if (!key || !window.confirm(`Revoke virtual key ${key.alias || key.id}?`)) return;
    try { await client.request(`/admin/v1/keys/${encodeURIComponent(key.id)}`, { method: "DELETE" }); navigate("/api-keys"); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not revoke virtual key"); }
  }
  async function copyToken() {
    if (!issued) return;
    setCopyError("");
    try { await navigator.clipboard.writeText(issued.token); setCopied(true); }
    catch { setCopyError("Clipboard access failed. Copy the token manually before closing this window."); }
  }

  if (loading && !key) return <LoadingState />;
  if (error && !key) return <ErrorState message={error} retry={() => void load()} />;
  if (!key) return <ErrorState message="Virtual key was not found" retry={() => void load()} />;
  const actionItems = [
    { label: "Edit settings", onSelect: () => navigate(`/api-keys?key_id=${encodeURIComponent(key.id)}&edit=1`) },
    { label: "Rotate", onSelect: rotate },
    ...(!key.revoked_at ? [{ label: key.disabled_at ? "Enable" : "Disable", onSelect: toggle }] : []),
    { label: "Revoke", tone: "danger" as const, onSelect: revoke }
  ];

  return <><PageHeader eyebrow="Access control" title={key.alias || key.id} description="Virtual-key ownership, effective grants, usage, budgets and lifecycle." actions={<div className="inline-actions"><button className="secondary" onClick={() => navigate("/api-keys")}>Back to virtual keys</button><ActionsMenu label={`Actions for ${key.alias || key.id}`} items={actionItems} /></div>} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Status" value={status} /><StatCard label="Requests (30d)" value={requests.toLocaleString()} detail={`${errors.toLocaleString()} failed`} /><StatCard label="Tokens (30d)" value={tokens.toLocaleString()} /><StatCard label="Spend (30d)" value={amounts(totals)} /></div>
    <div className="page-tabs" role="tablist" aria-label="Virtual key details"><button role="tab" aria-selected={tab === "overview"} className={tab === "overview" ? "active" : ""} onClick={() => setTab("overview")}>Overview</button><button role="tab" aria-selected={tab === "usage"} className={tab === "usage" ? "active" : ""} onClick={() => setTab("usage")}>Usage & budgets</button><button role="tab" aria-selected={tab === "settings"} className={tab === "settings" ? "active" : ""} onClick={() => setTab("settings")}>Settings</button></div>
    {tab === "overview" && <><section className="table-card access-group-details"><h2>Identity and ownership</h2><dl className="detail-grid"><div><dt>Key ID</dt><dd><code>{key.id}</code></dd></div><div><dt>Owner</dt><dd>{ownerType}: {owner?.name || owner?.email || ownerID}<br/><span className="muted">{ownerID}</span></dd></div><div><dt>Created</dt><dd>{formatTimestamp(key.created_at)}</dd></div><div><dt>Expires</dt><dd>{key.expires_at ? formatTimestamp(key.expires_at) : "Never"}</dd></div><div><dt>Description</dt><dd>{key.description || "—"}</dd></div><div><dt>Lifecycle</dt><dd>{key.revoked_at ? `Revoked ${formatTimestamp(key.revoked_at)}` : key.disabled_at ? `Disabled ${formatTimestamp(key.disabled_at)}` : "Authorizing"}</dd></div></dl></section><section className="section-block split-grid"><div className="notice-card"><h2>Models</h2>{values(key.allowed_models, "No direct model grants")}</div><div className="notice-card"><h2>Tools</h2>{values(key.allowed_tools, "No direct tool grants")}</div><div className="notice-card"><h2>Access groups</h2>{values(key.access_group_ids?.map((groupID) => directories.groups.get(groupID)?.name || groupID), "No access groups")}</div><div className="notice-card"><h2>Roles and tags</h2>{values([...(key.roles || []), ...(key.tags || []).map((tag) => `tag:${tag}`)], "No roles or tags")}</div></section></>}
    {tab === "usage" && <><section className="section-block"><h2>Applicable budget policies</h2><p>Every enabled policy is enforced independently; currencies are never combined.</p><ManagedDataTable rows={budgetRows} columns={[{ key: "scope", label: "Scope" }, { key: "period", label: "Period" }, { key: "limit", label: "Limit" }, { key: "used", label: "Used" }, { key: "remaining", label: "Remaining" }, { key: "reset", label: "Reset" }, { key: "status", label: "Status" }]} searchPlaceholder="Search budget policies" onRefresh={load} /></section><section className="section-block"><h2>Daily activity</h2><p>Server-filtered activity for this key over the last 30 days.</p><ManagedDataTable rows={dailyRows} columns={[{ key: "date", label: "Date" }, { key: "requests", label: "Requests" }, { key: "errors", label: "Errors" }, { key: "tokens", label: "Tokens" }, { key: "spend", label: "Spend" }, { key: "latency", label: "Average latency" }]} searchPlaceholder="Search daily activity" onRefresh={load} /></section></>}
    {tab === "settings" && <section className="table-card access-group-details"><div className="modal-heading"><h2>Effective key settings</h2><button onClick={() => navigate(`/api-keys?key_id=${encodeURIComponent(key.id)}&edit=1`)}>Edit settings</button></div><dl className="detail-grid"><div><dt>RPM limit</dt><dd>{key.rate_limit_rpm || "Unlimited"}</dd></div><div><dt>TPM limit</dt><dd>{key.rate_limit_tpm || "Unlimited"}</dd></div><div><dt>Models</dt><dd>{(key.allowed_models || []).join(", ") || "None"}</dd></div><div><dt>Tools</dt><dd>{(key.allowed_tools || []).join(", ") || "None"}</dd></div><div><dt>Access groups</dt><dd>{(key.access_group_ids || []).join(", ") || "None"}</dd></div><div><dt>Roles</dt><dd>{(key.roles || []).join(", ") || "None"}</dd></div></dl></section>}
    {issued && <div className="modal-backdrop" role="presentation"><section className="modal issued-key-modal" role="dialog" aria-modal="true" aria-label="Virtual key rotated"><div className="modal-heading"><h2>Save this rotated key now</h2><button className="icon-button" aria-label="Close issued key" onClick={() => setIssued(undefined)}>×</button></div><p>This plaintext token is shown exactly once. It cannot be recovered after this window is closed.</p><label>Virtual key<input readOnly value={issued.token} onFocus={(event) => event.currentTarget.select()} /></label>{copyError && <p className="form-error" role="alert">{copyError}</p>}{copied && <div className="copy-confirmation" role="status"><span aria-hidden="true">✓</span> Copied to clipboard</div>}<div className="modal-actions"><button className="secondary" onClick={() => setIssued(undefined)}>Close</button><button onClick={() => void copyToken()}>{copied ? "Copy again" : "Copy"}</button></div></section></div>}
  </>;
}
