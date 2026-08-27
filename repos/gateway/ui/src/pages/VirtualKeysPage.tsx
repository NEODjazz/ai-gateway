import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { formatTimestamp } from "../format";

type VirtualKey = {
  id: string; alias?: string; description?: string; tags?: string[]; user_id: string; team_id?: string; roles?: string[];
  allowed_models?: string[]; allowed_tools?: string[]; rate_limit_rpm?: number; rate_limit_tpm?: number; expires_at?: string;
  revoked_at?: string; disabled_at?: string; created_at: string;
};
type IssuedKey = { id: string; token: string; expires_at?: string };
type User = { id: string; name?: string; email?: string; team_ids?: string[]; status: string };
type Team = { id: string; name?: string; status: string };
type Organization = { id: string; name?: string; team_ids?: string[]; status: string };
type Model = { id?: string; model?: string };
type Budget = { id: number; scope_type: string; scope_id: string; period: string; currency: string; max_cost?: number; max_tokens?: number; enabled: boolean };
type SortKey = "key" | "team" | "user" | "created" | "budget";
type FilterState = { organization: string; team: string; user: string; key: string };
type FormState = {
  alias: string; description: string; organization: string; team_id: string; user_id: string; roles: string;
  allowed_models: string[]; allowed_tools: string; rate_limit_rpm: string; rate_limit_tpm: string; expires_at: string;
};

const emptyFilter: FilterState = { organization: "", team: "", user: "", key: "" };
const emptyForm: FormState = { alias: "", description: "", organization: "", team_id: "", user_id: "", roles: "", allowed_models: [], allowed_tools: "", rate_limit_rpm: "", rate_limit_tpm: "", expires_at: "" };

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}
function csv(value: string) { return value.split(",").map((part) => part.trim()).filter(Boolean); }
function keyStatus(row: VirtualKey) { return row.revoked_at ? "revoked" : row.disabled_at ? "disabled" : "active"; }
function userLabel(user: User) { return [user.name || user.email || user.id, user.id].filter((value, index, all) => value && all.indexOf(value) === index).join(" · "); }

function budgetFor(row: VirtualKey, budgets: Budget[]) {
  return budgets.filter((budget) => budget.enabled).map((budget) => {
    let score = 0;
    if (budget.scope_type === "key" && budget.scope_id === row.id) score = 4;
    else if (budget.scope_type === "user" && budget.scope_id === row.user_id) score = 3;
    else if (budget.scope_type === "team" && budget.scope_id === row.team_id) score = 2;
    else if (budget.scope_type === "global") score = 1;
    return { budget, score };
  }).filter((item) => item.score).sort((a, b) => b.score - a.score || a.budget.id - b.budget.id)[0]?.budget;
}
function budgetValue(budget?: Budget) { return budget?.max_cost ?? budget?.max_tokens ?? Number.POSITIVE_INFINITY; }
function budgetLabel(budget?: Budget) {
  if (!budget) return "—";
  if (budget.max_cost !== undefined) return `${budget.max_cost.toLocaleString()} ${budget.currency} / ${budget.period}`;
  return `${Number(budget.max_tokens || 0).toLocaleString()} tokens / ${budget.period}`;
}
function policyPayload(value: FormState) {
  return {
    alias: value.alias.trim(), description: value.description.trim(), user_id: value.user_id, team_id: value.team_id,
    roles: csv(value.roles), allowed_models: value.allowed_models, allowed_tools: csv(value.allowed_tools),
    rate_limit_rpm: Number(value.rate_limit_rpm || 0), rate_limit_tpm: Number(value.rate_limit_tpm || 0),
    expires_at: value.expires_at ? new Date(value.expires_at).toISOString() : undefined
  };
}
function existingPolicyPayload(row: VirtualKey) {
  return { alias: row.alias || "", description: row.description || "", tags: row.tags || [], user_id: row.user_id, team_id: row.team_id || "", roles: row.roles || [], allowed_models: row.allowed_models || [], allowed_tools: row.allowed_tools || [], rate_limit_rpm: row.rate_limit_rpm || 0, rate_limit_tpm: row.rate_limit_tpm || 0, expires_at: row.expires_at };
}

function RefreshIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>; }
function FilterIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M4 5h16l-6 7v5l-4 2v-7L4 5z" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" /></svg>; }

function KeyForm({ initial, users, teams, organizations, models, onClose, onSubmit }: { initial?: VirtualKey; users: User[]; teams: Team[]; organizations: Organization[]; models: string[]; onClose: () => void; onSubmit: (value: FormState) => Promise<void> }) {
  const initialOrganization = organizations.find((organization) => organization.team_ids?.includes(initial?.team_id || ""))?.id || "";
  const [value, setValue] = useState<FormState>(() => initial ? { alias: initial.alias || "", description: initial.description || "", organization: initialOrganization, team_id: initial.team_id || "", user_id: initial.user_id, roles: (initial.roles || []).join(", "), allowed_models: initial.allowed_models || [], allowed_tools: (initial.allowed_tools || []).join(", "), rate_limit_rpm: String(initial.rate_limit_rpm || ""), rate_limit_tpm: String(initial.rate_limit_tpm || ""), expires_at: initial.expires_at ? initial.expires_at.slice(0, 16) : "" } : emptyForm);
  const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  const organizationTeams = value.organization ? organizations.find((item) => item.id === value.organization)?.team_ids || [] : teams.map((team) => team.id);
  const availableTeams = teams.filter((team) => organizationTeams.includes(team.id));
  const availableUsers = users.filter((user) => value.team_id ? user.team_ids?.includes(value.team_id) : value.organization ? user.team_ids?.some((id) => organizationTeams.includes(id)) : true);
  function change(patch: Partial<FormState>) { setValue((current) => ({ ...current, ...patch })); }
  async function submit(event: FormEvent) { event.preventDefault(); setSaving(true); setError(""); try { await onSubmit(value); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save virtual key"); } finally { setSaving(false); } }
  return <div className="modal-backdrop" role="presentation"><section className="modal key-form-modal" role="dialog" aria-modal="true" aria-label={initial ? "Edit virtual key" : "Create Virtual Key"}><div className="modal-heading"><h2>{initial ? "Edit virtual key" : "Create Virtual Key"}</h2><button className="icon-button" aria-label="Close" onClick={onClose}>×</button></div><form onSubmit={submit} className="key-form">
    <label><span>Alias</span><input required value={value.alias} onChange={(event) => change({ alias: event.target.value })} /></label>
    <label><span>Description</span><textarea rows={3} value={value.description} onChange={(event) => change({ description: event.target.value })} /></label>
    <label><span>Organization</span><select aria-label="Organization" value={value.organization} onChange={(event) => change({ organization: event.target.value, team_id: "", user_id: "" })}><option value="">All organizations</option>{organizations.map((item) => <option key={item.id} value={item.id}>{item.name || item.id} · {item.id}</option>)}</select></label>
    <label><span>Team</span><select aria-label="Team" value={value.team_id} onChange={(event) => change({ team_id: event.target.value, user_id: "" })}><option value="">No team</option>{availableTeams.map((item) => <option key={item.id} value={item.id}>{item.name || item.id} · {item.id}</option>)}</select></label>
    <label><span>User</span><select aria-label="User" required value={value.user_id} onChange={(event) => change({ user_id: event.target.value })}><option value="">Select user</option>{availableUsers.map((item) => <option key={item.id} value={item.id}>{userLabel(item)}</option>)}</select></label>
    <label><span>Models</span><select aria-label="Models" multiple size={Math.min(8, Math.max(4, models.length))} value={value.allowed_models} onChange={(event) => change({ allowed_models: Array.from(event.target.selectedOptions, (option) => option.value) })}>{models.map((model) => <option key={model} value={model}>{model}</option>)}</select></label>
    <label><span>Roles</span><input placeholder="developer, viewer" value={value.roles} onChange={(event) => change({ roles: event.target.value })} /></label>
    <label><span>Allowed tools</span><input placeholder="tool-a, tool-b" value={value.allowed_tools} onChange={(event) => change({ allowed_tools: event.target.value })} /></label>
    <label><span>Rate limit RPM</span><input type="number" min="0" value={value.rate_limit_rpm} onChange={(event) => change({ rate_limit_rpm: event.target.value })} /></label>
    <label><span>Rate limit TPM</span><input type="number" min="0" value={value.rate_limit_tpm} onChange={(event) => change({ rate_limit_tpm: event.target.value })} /></label>
    <label><span>Expires at</span><input type="datetime-local" value={value.expires_at} onChange={(event) => change({ expires_at: event.target.value })} /></label>
    {error && <p className="form-error key-form-error" role="alert">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button disabled={saving}>{saving ? "Saving…" : initial ? "Save changes" : "Generate key"}</button></div>
  </form></section></div>;
}

function FilterModal({ value, organizations, teams, users, onChange, onClose }: { value: FilterState; organizations: Organization[]; teams: Team[]; users: User[]; onChange: (value: FilterState) => void; onClose: () => void }) {
  const [draft, setDraft] = useState(value);
  const organizationTeams = draft.organization ? organizations.find((item) => item.id === draft.organization)?.team_ids || [] : teams.map((team) => team.id);
  const availableTeams = teams.filter((team) => organizationTeams.includes(team.id));
  const availableUsers = users.filter((user) => draft.team ? user.team_ids?.includes(draft.team) : draft.organization ? user.team_ids?.some((id) => organizationTeams.includes(id)) : true);
  return <div className="modal-backdrop" role="presentation"><section className="modal key-filter-modal" role="dialog" aria-modal="true" aria-label="Filter virtual keys"><div className="modal-heading"><h2>Filter virtual keys</h2><button className="icon-button" aria-label="Close filters" onClick={onClose}>×</button></div><div className="form-card"><label>Organization<select value={draft.organization} onChange={(event) => setDraft({ ...draft, organization: event.target.value, team: "", user: "" })}><option value="">All organizations</option>{organizations.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label><label>Team<select value={draft.team} onChange={(event) => setDraft({ ...draft, team: event.target.value, user: "" })}><option value="">All teams</option>{availableTeams.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label><label>User<select value={draft.user} onChange={(event) => setDraft({ ...draft, user: event.target.value })}><option value="">All users</option>{availableUsers.map((item) => <option key={item.id} value={item.id}>{userLabel(item)}</option>)}</select></label><label>Key ID<input value={draft.key} onChange={(event) => setDraft({ ...draft, key: event.target.value })} /></label></div><div className="modal-actions"><button className="secondary" onClick={() => { setDraft(emptyFilter); onChange(emptyFilter); onClose(); }}>Reset filters</button><button onClick={() => { onChange(draft); onClose(); }}>Apply filters</button></div></section></div>;
}

export function VirtualKeysPage() {
  const { client } = useAuth();
  const [keys, setKeys] = useState<VirtualKey[]>([]); const [users, setUsers] = useState<User[]>([]); const [teams, setTeams] = useState<Team[]>([]); const [organizations, setOrganizations] = useState<Organization[]>([]); const [models, setModels] = useState<string[]>([]); const [budgets, setBudgets] = useState<Budget[]>([]);
  const [search, setSearch] = useState(""); const [filters, setFilters] = useState(emptyFilter); const [filterOpen, setFilterOpen] = useState(false); const [form, setForm] = useState<"create" | VirtualKey>(); const [issued, setIssued] = useState<IssuedKey>(); const [copied, setCopied] = useState(false); const [copyError, setCopyError] = useState("");
  const [sort, setSort] = useState<SortKey>("created"); const [direction, setDirection] = useState<"asc" | "desc">("desc"); const [loading, setLoading] = useState(true); const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try {
      const keyPayload = await client.request("/admin/v1/keys?limit=500");
      const safe = async (path: string) => { try { return await client.request(path); } catch { return { data: [] }; } };
      const [userPayload, teamPayload, organizationPayload, modelPayload, budgetPayload] = await Promise.all([safe("/admin/v1/users?limit=500"), safe("/admin/v1/teams?limit=500"), safe("/admin/v1/organizations?limit=500"), safe("/v1/models"), safe("/admin/v1/budgets")]);
      setKeys(records<VirtualKey>(keyPayload)); setUsers(records<User>(userPayload)); setTeams(records<Team>(teamPayload)); setOrganizations(records<Organization>(organizationPayload)); setBudgets(records<Budget>(budgetPayload));
      setModels([...new Set(records<Model>(modelPayload).map((model) => model.id || model.model || "").filter(Boolean))].sort());
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load virtual keys"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  const rows = useMemo(() => {
    const organizationTeams = filters.organization ? organizations.find((item) => item.id === filters.organization)?.team_ids || [] : [];
    const filtered = keys.filter((row) => (!search.trim() || (row.alias || "").toLowerCase().includes(search.trim().toLowerCase())) && (!filters.organization || organizationTeams.includes(row.team_id || "")) && (!filters.team || row.team_id === filters.team) && (!filters.user || row.user_id === filters.user) && (!filters.key || row.id.toLowerCase().includes(filters.key.toLowerCase())));
    const compare = (a: VirtualKey, b: VirtualKey) => {
      if (sort === "created") return new Date(a.created_at).getTime() - new Date(b.created_at).getTime();
      if (sort === "budget") { const left = budgetValue(budgetFor(a, budgets)); const right = budgetValue(budgetFor(b, budgets)); return left === right ? 0 : left < right ? -1 : 1; }
      const left = sort === "key" ? a.id : sort === "team" ? a.team_id || "" : a.user_id;
      const right = sort === "key" ? b.id : sort === "team" ? b.team_id || "" : b.user_id;
      return left.localeCompare(right);
    };
    return [...filtered].sort((a, b) => direction === "asc" ? compare(a, b) : compare(b, a));
  }, [budgets, direction, filters, keys, organizations, search, sort]);
  const usersByID = useMemo(() => new Map(users.map((user) => [user.id, user])), [users]);
  const teamsByID = useMemo(() => new Map(teams.map((team) => [team.id, team])), [teams]);
  function sortBy(next: SortKey) { if (sort === next) setDirection((current) => current === "asc" ? "desc" : "asc"); else { setSort(next); setDirection(next === "created" ? "desc" : "asc"); } }
  function sortHeading(label: string, key: SortKey) { return <button className="sort-button" onClick={() => sortBy(key)}>{label}<span aria-hidden="true">{sort === key ? direction === "asc" ? " ↑" : " ↓" : " ↕"}</span></button>; }
  async function saveKey(value: FormState) {
    if (form === "create") { const result = await client.request<IssuedKey>("/admin/v1/keys", { method: "POST", body: policyPayload(value) }); setForm(undefined); setIssued(result); setCopied(false); setCopyError(""); }
    else if (form) { await client.request(`/admin/v1/keys/${encodeURIComponent(form.id)}`, { method: "PUT", body: policyPayload(value) }); setForm(undefined); }
    await load();
  }
  async function rotate(row: VirtualKey) { try { const result = await client.request<IssuedKey>(`/admin/v1/keys/${encodeURIComponent(row.id)}/rotate`, { method: "POST", body: existingPolicyPayload(row) }); setIssued(result); setCopied(false); setCopyError(""); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not rotate key"); } }
  async function toggle(row: VirtualKey) { try { await client.request(`/admin/v1/keys/${encodeURIComponent(row.id)}/${row.disabled_at ? "enable" : "disable"}`, { method: "POST", body: {} }); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not change key status"); } }
  async function revoke(row: VirtualKey) { if (!window.confirm(`Revoke virtual key ${row.alias || row.id}?`)) return; try { await client.request(`/admin/v1/keys/${encodeURIComponent(row.id)}`, { method: "DELETE" }); await load(); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not revoke key"); } }
  async function copyToken() { if (!issued) return; setCopyError(""); try { await navigator.clipboard.writeText(issued.token); setCopied(true); } catch { setCopyError("Clipboard access failed. Copy the token manually before closing this window."); } }
  const activeFilters = Object.values(filters).filter(Boolean).length;
  return <><PageHeader eyebrow="Access control" title="Virtual keys" description="Issue scoped, revocable gateway credentials. Plaintext tokens are displayed exactly once." />
    <div className="key-toolbar"><button onClick={() => setForm("create")}>Create Virtual Key</button><div className="key-toolbar-right"><label className="key-search"><span className="sr-only">Search keys by alias</span><input aria-label="Search keys by alias" placeholder="Search by alias" value={search} onChange={(event) => setSearch(event.target.value)} /></label><button className="secondary icon-text-button" onClick={() => setFilterOpen(true)}><FilterIcon />Filter{activeFilters ? ` (${activeFilters})` : ""}</button><button className="secondary icon-only-button" aria-label="Refresh virtual keys" title="Refresh" onClick={() => void load()}><RefreshIcon /></button></div></div>
    {error && <ErrorState message={error} retry={() => void load()} />}{loading ? <LoadingState /> : <div className="table-card"><div className="table-scroll"><table><thead><tr><th>{sortHeading("Key", "key")}</th><th>Alias</th><th>{sortHeading("Team", "team")}</th><th>{sortHeading("User", "user")}</th><th>{sortHeading("Created", "created")}</th><th>{sortHeading("Budget", "budget")}</th><th>Status</th><th>Models</th><th><span className="sr-only">Actions</span></th></tr></thead><tbody>{rows.length ? rows.map((row) => { const budget = budgetFor(row, budgets); const status = keyStatus(row); const team = teamsByID.get(row.team_id || ""); const user = usersByID.get(row.user_id); return <tr key={row.id}><td><code>{row.id}</code></td><td><strong>{row.alias || "—"}</strong></td><td>{row.team_id ? <><strong>{team?.name || row.team_id}</strong>{team?.name && <><br/><span className="muted">{row.team_id}</span></>}</> : "—"}</td><td><strong>{user?.name || user?.email || row.user_id}</strong>{(user?.name || user?.email) && <><br/><span className="muted">{row.user_id}</span></>}</td><td>{formatTimestamp(row.created_at)}</td><td>{budgetLabel(budget)}</td><td><span className={`status ${status === "active" ? "enabled" : "disabled"}`}>{status}</span></td><td><div className="tag-list">{(row.allowed_models || []).map((model) => <span className="tag" key={model}>{model}</span>)}</div></td><td className="row-actions"><div className="inline-actions"><button className="text-button" onClick={() => setForm(row)}>Edit</button><button className="text-button" onClick={() => void rotate(row)}>Rotate</button>{!row.revoked_at && <button className="text-button" onClick={() => void toggle(row)}>{row.disabled_at ? "Enable" : "Disable"}</button>}<button className="danger-button" onClick={() => void revoke(row)}>Revoke</button></div></td></tr>; }) : <tr><td colSpan={9}><div className="empty-table">No virtual keys match the current search and filters.</div></td></tr>}</tbody></table></div></div>}
    {form && <KeyForm initial={form === "create" ? undefined : form} users={users} teams={teams} organizations={organizations} models={models} onClose={() => setForm(undefined)} onSubmit={saveKey} />}
    {filterOpen && <FilterModal value={filters} organizations={organizations} teams={teams} users={users} onChange={setFilters} onClose={() => setFilterOpen(false)} />}
    {issued && <div className="modal-backdrop" role="presentation"><section className="modal issued-key-modal" role="dialog" aria-modal="true" aria-label="Virtual key created"><div className="modal-heading"><h2>Save this key now</h2><button className="icon-button" aria-label="Close issued key" onClick={() => setIssued(undefined)}>×</button></div><p>This plaintext token is shown exactly once. It cannot be recovered after this window is closed.</p><label>Virtual key<input readOnly value={issued.token} onFocus={(event) => event.currentTarget.select()} /></label>{copyError && <p className="form-error" role="alert">{copyError}</p>}{copied && <div className="copy-confirmation" role="status"><span aria-hidden="true">✓</span> Copied to clipboard</div>}<div className="modal-actions"><button className="secondary" onClick={() => setIssued(undefined)}>Close</button><button onClick={() => void copyToken()}>{copied ? "Copy again" : "Copy"}</button></div></section></div>}
  </>;
}
