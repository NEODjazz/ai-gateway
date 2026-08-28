import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { formatTimestamp } from "../format";

type VirtualKey = {
	id: string; alias?: string; description?: string; tags?: string[]; user_id?: string; team_id?: string; organization_id?: string; roles?: string[];
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
	const owner = value.user_id ? { user_id: value.user_id } : value.team_id ? { team_id: value.team_id } : { organization_id: value.organization };
	return {
		alias: value.alias.trim(), description: value.description.trim(), ...owner,
    roles: csv(value.roles), allowed_models: value.allowed_models, allowed_tools: csv(value.allowed_tools),
    rate_limit_rpm: Number(value.rate_limit_rpm || 0), rate_limit_tpm: Number(value.rate_limit_tpm || 0),
    expires_at: value.expires_at ? new Date(value.expires_at).toISOString() : undefined
  };
}
function existingPolicyPayload(row: VirtualKey) {
	const owner = row.user_id ? { user_id: row.user_id } : row.team_id ? { team_id: row.team_id } : { organization_id: row.organization_id || "" };
	return { alias: row.alias || "", description: row.description || "", tags: row.tags || [], ...owner, roles: row.roles || [], allowed_models: row.allowed_models || [], allowed_tools: row.allowed_tools || [], rate_limit_rpm: row.rate_limit_rpm || 0, rate_limit_tpm: row.rate_limit_tpm || 0, expires_at: row.expires_at };
}

function RefreshIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>; }
function FilterIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M4 5h16l-6 7v5l-4 2v-7L4 5z" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" /></svg>; }

function ModelMultiSelect({ models, value, onChange }: { models: string[]; value: string[]; onChange: (models: string[]) => void }) {
	const [query, setQuery] = useState(""); const [open, setOpen] = useState(false);
	const available = models.filter((model) => !value.includes(model) && model.toLowerCase().includes(query.trim().toLowerCase()));
	function select(model: string) { onChange([...value, model]); setQuery(""); setOpen(true); }
	return <div className="key-model-field"><span id="key-model-label">Models</span><div className="model-multi-select" onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false); }}>
		<div className="model-multi-control" onClick={() => setOpen(true)}>{value.map((model) => <span className="model-chip" key={model}>{model}<button type="button" aria-label={`Remove model ${model}`} onClick={(event) => { event.stopPropagation(); onChange(value.filter((item) => item !== model)); }}>×</button></span>)}<input aria-labelledby="key-model-label" aria-label="Models" role="combobox" aria-expanded={open} aria-controls="key-model-options" placeholder={value.length ? "Select another model…" : "Search and select models…"} value={query} onFocus={() => setOpen(true)} onChange={(event) => { setQuery(event.target.value); setOpen(true); }} onKeyDown={(event) => { if (event.key === "Enter" && available[0]) { event.preventDefault(); select(available[0]); } else if (event.key === "Backspace" && !query && value.length) onChange(value.slice(0, -1)); else if (event.key === "Escape") setOpen(false); }} /></div>
		{open && <div className="model-multi-options" id="key-model-options" role="listbox" aria-label="Available models">{available.length ? available.map((model) => <button type="button" role="option" aria-selected="false" key={model} onMouseDown={(event) => event.preventDefault()} onClick={() => select(model)}>{model}</button>) : <span>{models.length ? "No more matching models" : "No configured models"}</span>}</div>}
	</div></div>;
}

function KeyForm({ initial, users, teams, organizations, models, onClose, onSubmit }: { initial?: VirtualKey; users: User[]; teams: Team[]; organizations: Organization[]; models: string[]; onClose: () => void; onSubmit: (value: FormState) => Promise<void> }) {
	const initialOrganization = initial?.organization_id || organizations.find((organization) => organization.team_ids?.includes(initial?.team_id || ""))?.id || "";
	const [value, setValue] = useState<FormState>(() => initial ? { alias: initial.alias || "", description: initial.description || "", organization: initialOrganization, team_id: initial.team_id || "", user_id: initial.user_id || "", roles: (initial.roles || []).join(", "), allowed_models: initial.allowed_models || [], allowed_tools: (initial.allowed_tools || []).join(", "), rate_limit_rpm: String(initial.rate_limit_rpm || ""), rate_limit_tpm: String(initial.rate_limit_tpm || ""), expires_at: initial.expires_at ? initial.expires_at.slice(0, 16) : "" } : emptyForm);
  const [saving, setSaving] = useState(false); const [error, setError] = useState("");
  const organizationTeams = value.organization ? organizations.find((item) => item.id === value.organization)?.team_ids || [] : teams.map((team) => team.id);
  const availableTeams = teams.filter((team) => organizationTeams.includes(team.id));
  const availableUsers = users.filter((user) => value.team_id ? user.team_ids?.includes(value.team_id) : value.organization ? user.team_ids?.some((id) => organizationTeams.includes(id)) : true);
  function change(patch: Partial<FormState>) { setValue((current) => ({ ...current, ...patch })); }
	async function submit(event: FormEvent) { event.preventDefault(); setError(""); if (!value.organization && !value.team_id && !value.user_id) { setError("Select an organization, team, or user as the key owner."); return; } setSaving(true); try { await onSubmit(value); } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save virtual key"); } finally { setSaving(false); } }
  return <div className="modal-backdrop" role="presentation"><section className="modal key-form-modal" role="dialog" aria-modal="true" aria-label={initial ? "Edit virtual key" : "Create Virtual Key"}><div className="modal-heading"><h2>{initial ? "Edit virtual key" : "Create Virtual Key"}</h2><button className="icon-button" aria-label="Close" onClick={onClose}>×</button></div><form onSubmit={submit} className="key-form">
    <label><span>Alias</span><input required value={value.alias} onChange={(event) => change({ alias: event.target.value })} /></label>
    <label><span>Description</span><textarea rows={3} value={value.description} onChange={(event) => change({ description: event.target.value })} /></label>
		<label><span>Organization</span><select aria-label="Organization" value={value.organization} onChange={(event) => change({ organization: event.target.value, team_id: "", user_id: "" })}><option value="">Select organization (optional)</option>{organizations.map((item) => <option key={item.id} value={item.id}>{item.name || item.id} · {item.id}</option>)}</select></label>
		<label><span>Team</span><select aria-label="Team" value={value.team_id} onChange={(event) => change({ team_id: event.target.value, user_id: "" })}><option value="">No team — use organization owner</option>{availableTeams.map((item) => <option key={item.id} value={item.id}>{item.name || item.id} · {item.id}</option>)}</select></label>
		<label><span>User</span><select aria-label="User" value={value.user_id} onChange={(event) => change({ user_id: event.target.value })}><option value="">No user — use team or organization owner</option>{availableUsers.map((item) => <option key={item.id} value={item.id}>{userLabel(item)}</option>)}</select></label>
		<ModelMultiSelect models={models} value={value.allowed_models} onChange={(allowed_models) => change({ allowed_models })} />
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
	const [page, setPage] = useState(0); const [pageSizeOption, setPageSizeOption] = useState("25"); const [customPageSize, setCustomPageSize] = useState("25");
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
		const userTeams = new Map(users.map((user) => [user.id, user.team_ids || []]));
		const belongsToTeam = (row: VirtualKey, teamID: string) => row.team_id === teamID || Boolean(row.user_id && userTeams.get(row.user_id)?.includes(teamID));
		const belongsToOrganization = (row: VirtualKey) => row.organization_id === filters.organization || Boolean(row.team_id && organizationTeams.includes(row.team_id)) || Boolean(row.user_id && userTeams.get(row.user_id)?.some((teamID) => organizationTeams.includes(teamID)));
		const filtered = keys.filter((row) => (!search.trim() || (row.alias || "").toLowerCase().includes(search.trim().toLowerCase())) && (!filters.organization || belongsToOrganization(row)) && (!filters.team || belongsToTeam(row, filters.team)) && (!filters.user || row.user_id === filters.user) && (!filters.key || row.id.toLowerCase().includes(filters.key.toLowerCase())));
    const compare = (a: VirtualKey, b: VirtualKey) => {
      if (sort === "created") return new Date(a.created_at).getTime() - new Date(b.created_at).getTime();
      if (sort === "budget") { const left = budgetValue(budgetFor(a, budgets)); const right = budgetValue(budgetFor(b, budgets)); return left === right ? 0 : left < right ? -1 : 1; }
		const left = sort === "key" ? a.id : sort === "team" ? a.team_id || "" : a.user_id || "";
		const right = sort === "key" ? b.id : sort === "team" ? b.team_id || "" : b.user_id || "";
      return left.localeCompare(right);
    };
    return [...filtered].sort((a, b) => direction === "asc" ? compare(a, b) : compare(b, a));
	}, [budgets, direction, filters, keys, organizations, search, sort, users]);
	const usersByID = useMemo(() => new Map(users.map((user) => [user.id, user])), [users]);
	const teamsByID = useMemo(() => new Map(teams.map((team) => [team.id, team])), [teams]);
	const organizationsByID = useMemo(() => new Map(organizations.map((organization) => [organization.id, organization])), [organizations]);
	const pageSize = pageSizeOption === "custom" ? Math.min(500, Math.max(1, Number(customPageSize) || 1)) : Number(pageSizeOption);
	const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
	const visibleRows = rows.slice(page * pageSize, (page + 1) * pageSize);
	useEffect(() => { setPage(0); }, [customPageSize, direction, filters, keys, pageSizeOption, search, sort]);
	useEffect(() => { if (page >= pageCount) setPage(pageCount - 1); }, [page, pageCount]);
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
		{error && <ErrorState message={error} retry={() => void load()} />}{loading ? <LoadingState /> : <><div className="table-card"><div className="table-scroll"><table><thead><tr><th>{sortHeading("Key", "key")}</th><th>Alias</th><th>Organization</th><th>{sortHeading("Team", "team")}</th><th>{sortHeading("User", "user")}</th><th>{sortHeading("Created", "created")}</th><th>{sortHeading("Budget", "budget")}</th><th>Status</th><th>Models</th><th><span className="sr-only">Actions</span></th></tr></thead><tbody>{visibleRows.length ? visibleRows.map((row) => { const budget = budgetFor(row, budgets); const status = keyStatus(row); const team = teamsByID.get(row.team_id || ""); const user = usersByID.get(row.user_id || ""); const organization = organizationsByID.get(row.organization_id || ""); return <tr key={row.id}><td><code>{row.id}</code></td><td><strong>{row.alias || "—"}</strong></td><td>{row.organization_id ? <><strong>{organization?.name || row.organization_id}</strong>{organization?.name && <><br/><span className="muted">{row.organization_id}</span></>}</> : "—"}</td><td>{row.team_id ? <><strong>{team?.name || row.team_id}</strong>{team?.name && <><br/><span className="muted">{row.team_id}</span></>}</> : "—"}</td><td>{row.user_id ? <><strong>{user?.name || user?.email || row.user_id}</strong>{(user?.name || user?.email) && <><br/><span className="muted">{row.user_id}</span></>}</> : "—"}</td><td>{formatTimestamp(row.created_at)}</td><td>{budgetLabel(budget)}</td><td><span className={`status ${status === "active" ? "enabled" : "disabled"}`}>{status}</span></td><td><div className="tag-list">{(row.allowed_models || []).map((model) => <span className="tag" key={model}>{model}</span>)}</div></td><td className="row-actions"><div className="inline-actions"><button className="text-button" onClick={() => setForm(row)}>Edit</button><button className="text-button" onClick={() => void rotate(row)}>Rotate</button>{!row.revoked_at && <button className="text-button" onClick={() => void toggle(row)}>{row.disabled_at ? "Enable" : "Disable"}</button>}<button className="danger-button" onClick={() => void revoke(row)}>Revoke</button></div></td></tr>; }) : <tr><td colSpan={10}><div className="empty-table">No virtual keys match the current search and filters.</div></td></tr>}</tbody></table></div></div><div className="key-pagination"><div className="key-pagination-summary"><label>Rows per page<select aria-label="Rows per page" value={pageSizeOption} onChange={(event) => setPageSizeOption(event.target.value)}>{[10, 25, 50, 100].map((size) => <option value={size} key={size}>{size}</option>)}<option value="custom">Custom</option></select></label>{pageSizeOption === "custom" && <label>Custom rows<input aria-label="Custom rows per page" type="number" min="1" max="500" value={customPageSize} onChange={(event) => setCustomPageSize(event.target.value)} /></label>}<span>{rows.length ? `${page * pageSize + 1}–${Math.min((page + 1) * pageSize, rows.length)} of ${rows.length}` : "0 results"}</span></div><div className="key-pagination-navigation"><button className="secondary" disabled={page === 0} onClick={() => setPage((current) => Math.max(0, current - 1))}>Previous</button><button className="secondary" disabled={page + 1 >= pageCount} onClick={() => setPage((current) => Math.min(pageCount - 1, current + 1))}>Next</button></div></div></>}
    {form && <KeyForm initial={form === "create" ? undefined : form} users={users} teams={teams} organizations={organizations} models={models} onClose={() => setForm(undefined)} onSubmit={saveKey} />}
    {filterOpen && <FilterModal value={filters} organizations={organizations} teams={teams} users={users} onChange={setFilters} onClose={() => setFilterOpen(false)} />}
    {issued && <div className="modal-backdrop" role="presentation"><section className="modal issued-key-modal" role="dialog" aria-modal="true" aria-label="Virtual key created"><div className="modal-heading"><h2>Save this key now</h2><button className="icon-button" aria-label="Close issued key" onClick={() => setIssued(undefined)}>×</button></div><p>This plaintext token is shown exactly once. It cannot be recovered after this window is closed.</p><label>Virtual key<input readOnly value={issued.token} onFocus={(event) => event.currentTarget.select()} /></label>{copyError && <p className="form-error" role="alert">{copyError}</p>}{copied && <div className="copy-confirmation" role="status"><span aria-hidden="true">✓</span> Copied to clipboard</div>}<div className="modal-actions"><button className="secondary" onClick={() => setIssued(undefined)}>Close</button><button onClick={() => void copyToken()}>{copied ? "Copy again" : "Copy"}</button></div></section></div>}
  </>;
}
