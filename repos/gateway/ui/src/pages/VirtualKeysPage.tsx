import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ActionsMenu, type ActionMenuItem } from "../components/ActionsMenu";
import { ColumnsMenu, type ColumnChoice } from "../components/ColumnsMenu";
import { ChipMultiSelect } from "../components/ChipMultiSelect";
import { PageHeader } from "../components/PageHeader";
import { formatCost, formatTimestamp } from "../format";

type VirtualKey = {
	id: string; alias?: string; description?: string; tags?: string[]; user_id?: string; team_id?: string; organization_id?: string; roles?: string[];
  access_group_ids?: string[]; allowed_models?: string[]; allowed_tools?: string[]; rate_limit_rpm?: number; rate_limit_tpm?: number; expires_at?: string;
  revoked_at?: string; disabled_at?: string; created_at: string;
};
type IssuedKey = { id: string; token: string; expires_at?: string };
type User = { id: string; name?: string; email?: string; team_ids?: string[]; status: string };
type Team = { id: string; name?: string; status: string };
type Organization = { id: string; name?: string; team_ids?: string[]; status: string };
type AccessGroup = { id: string; name: string; description?: string; project_id?: string; allowed_models?: string[]; allowed_tools?: string[]; enabled: boolean };
type Model = { id?: string; model?: string };
type BudgetPolicy = { id: number; scope_type: string; scope_id: string; period: string; currency: string; max_cost?: number; max_tokens?: number; enabled: boolean };
type BudgetSummary = { policy: BudgetPolicy; window_start: string; window_end: string; used_cost: number; remaining_cost?: number; used_tokens: number; remaining_tokens?: number };
type KeyBudgetProjection = { key_id: string; policies: BudgetSummary[] };
type KeyUsage = { name?: string; currency: string; requests: number; total_tokens: number; cost: number };
type SortKey = "key" | "team" | "user" | "created";
type FilterState = { organization: string; team: string; user: string; key: string; status: string };
type FormState = {
  alias: string; description: string; organization: string; team_id: string; user_id: string; roles: string;
  access_group_ids: string[]; allowed_models: string[]; allowed_tools: string; rate_limit_rpm: string; rate_limit_tpm: string; expires_at: string;
};

const emptyFilter: FilterState = { organization: "", team: "", user: "", key: "", status: "" };
const emptyForm: FormState = { alias: "", description: "", organization: "", team_id: "", user_id: "", roles: "", access_group_ids: [], allowed_models: [], allowed_tools: "", rate_limit_rpm: "", rate_limit_tpm: "", expires_at: "" };
const keyColumns: ColumnChoice[] = [
  { key: "key", label: "Key", locked: true }, { key: "alias", label: "Alias" }, { key: "organization", label: "Organization" },
  { key: "team", label: "Team" }, { key: "user", label: "User" }, { key: "created", label: "Created" },
  { key: "spend", label: "Spend (30d)" }, { key: "requests", label: "Requests (30d)" }, { key: "tokens", label: "Tokens (30d)" },
  { key: "budget", label: "Budgets" }, { key: "budget_used", label: "Budget used" }, { key: "budget_remaining", label: "Budget remaining" }, { key: "budget_reset", label: "Budget reset" },
  { key: "status", label: "Status" }, { key: "access_groups", label: "Access groups" }, { key: "models", label: "Models" }, { key: "description", label: "Description" },
  { key: "roles", label: "Roles" }, { key: "tools", label: "Tools" }, { key: "rpm", label: "RPM" }, { key: "tpm", label: "TPM" },
  { key: "tags", label: "Tags" }, { key: "expires", label: "Expires" }
];
const defaultKeyColumns = new Set(["key", "alias", "organization", "team", "user", "created", "spend", "budget", "status", "access_groups", "models"]);

function records<T>(payload: unknown): T[] {
  if (Array.isArray(payload)) return payload as T[];
  const data = payload && typeof payload === "object" ? (payload as { data?: unknown }).data : undefined;
  return Array.isArray(data) ? data as T[] : [];
}
function csv(value: string) { return value.split(",").map((part) => part.trim()).filter(Boolean); }
function keyStatus(row: VirtualKey) { return row.revoked_at ? "revoked" : row.disabled_at ? "disabled" : row.expires_at && new Date(row.expires_at) <= new Date() ? "expired" : "active"; }
function userLabel(user: User) { return [user.name || user.email || user.id, user.id].filter((value, index, all) => value && all.indexOf(value) === index).join(" · "); }

function budgetLines(projection: KeyBudgetProjection | undefined, mode: "limit" | "used" | "remaining" | "reset") {
  if (!projection?.policies.length) return [];
  if (mode === "reset") return [...new Set(projection.policies.map((summary) => summary.window_end))].sort().map(formatTimestamp);
  const lines: string[] = [];
  for (const summary of projection.policies) {
    const { policy } = summary; const scope = policy.scope_type === "global" ? "global" : `${policy.scope_type}:${policy.scope_id}`;
    if (policy.max_cost !== undefined) {
      const value = mode === "limit" ? policy.max_cost : mode === "used" ? summary.used_cost : summary.remaining_cost;
      lines.push(`${scope} · ${formatCost(Number(value || 0), policy.currency)}${mode === "limit" ? ` / ${policy.period}` : ""}`);
    }
    if (policy.max_tokens !== undefined) {
      const value = mode === "limit" ? policy.max_tokens : mode === "used" ? summary.used_tokens : summary.remaining_tokens;
      lines.push(`${scope} · ${Number(value || 0).toLocaleString()} tokens${mode === "limit" ? ` / ${policy.period}` : ""}`);
    }
  }
  return lines;
}
function BudgetCell({ projection, mode }: { projection?: KeyBudgetProjection; mode: "limit" | "used" | "remaining" | "reset" }) {
  const lines = budgetLines(projection, mode);
  return lines.length ? <div className="budget-values">{lines.map((line, index) => <span key={`${index}-${line}`}>{line}</span>)}</div> : <>—</>;
}
function usageForKey(id: string, usage: KeyUsage[]) {
  const rows = usage.filter((row) => row.name === id);
  const costs = new Map<string, number>();
  for (const row of rows) costs.set(row.currency || "USD", (costs.get(row.currency || "USD") || 0) + row.cost);
  return {
    requests: rows.reduce((sum, row) => sum + row.requests, 0),
    tokens: rows.reduce((sum, row) => sum + row.total_tokens, 0),
    spend: [...costs].sort(([left], [right]) => left.localeCompare(right)).map(([currency, cost]) => formatCost(cost, currency)).join(" · ") || "—"
  };
}
function policyPayload(value: FormState) {
	const owner = value.user_id ? { user_id: value.user_id } : value.team_id ? { team_id: value.team_id } : { organization_id: value.organization };
	return {
		alias: value.alias.trim(), description: value.description.trim(), ...owner, access_group_ids: value.access_group_ids,
    roles: csv(value.roles), allowed_models: value.allowed_models, allowed_tools: csv(value.allowed_tools),
    rate_limit_rpm: Number(value.rate_limit_rpm || 0), rate_limit_tpm: Number(value.rate_limit_tpm || 0),
    expires_at: value.expires_at ? new Date(value.expires_at).toISOString() : undefined
  };
}
function existingPolicyPayload(row: VirtualKey) {
	const owner = row.user_id ? { user_id: row.user_id } : row.team_id ? { team_id: row.team_id } : { organization_id: row.organization_id || "" };
	return { alias: row.alias || "", description: row.description || "", tags: row.tags || [], ...owner, roles: row.roles || [], access_group_ids: row.access_group_ids || [], allowed_models: row.allowed_models || [], allowed_tools: row.allowed_tools || [], rate_limit_rpm: row.rate_limit_rpm || 0, rate_limit_tpm: row.rate_limit_tpm || 0, expires_at: row.expires_at };
}

function RefreshIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>; }
function FilterIcon() { return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M4 5h16l-6 7v5l-4 2v-7L4 5z" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" /></svg>; }

function KeyForm({ initial, users, teams, organizations, accessGroups, models, onClose, onSubmit }: { initial?: VirtualKey; users: User[]; teams: Team[]; organizations: Organization[]; accessGroups: AccessGroup[]; models: string[]; onClose: () => void; onSubmit: (value: FormState) => Promise<void> }) {
	const initialOrganization = initial?.organization_id || organizations.find((organization) => organization.team_ids?.includes(initial?.team_id || ""))?.id || "";
	const [value, setValue] = useState<FormState>(() => initial ? { alias: initial.alias || "", description: initial.description || "", organization: initialOrganization, team_id: initial.team_id || "", user_id: initial.user_id || "", roles: (initial.roles || []).join(", "), access_group_ids: initial.access_group_ids || [], allowed_models: initial.allowed_models || [], allowed_tools: (initial.allowed_tools || []).join(", "), rate_limit_rpm: String(initial.rate_limit_rpm || ""), rate_limit_tpm: String(initial.rate_limit_tpm || ""), expires_at: initial.expires_at ? initial.expires_at.slice(0, 16) : "" } : emptyForm);
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
		<ChipMultiSelect label="Access groups" options={accessGroups.filter((group) => group.enabled).map((group) => ({ value: group.id, label: group.name || group.id, description: [group.project_id && `project ${group.project_id}`, `${group.allowed_models?.length || 0} model grants`, `${group.allowed_tools?.length || 0} tool grants`].filter(Boolean).join(" · ") }))} value={value.access_group_ids} onChange={(access_group_ids) => change({ access_group_ids })} />
		<ChipMultiSelect label="Models" options={models.map((model) => ({ value: model, label: model }))} value={value.allowed_models} onChange={(allowed_models) => change({ allowed_models })} />
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
  return <div className="modal-backdrop" role="presentation"><section className="modal key-filter-modal" role="dialog" aria-modal="true" aria-label="Filter virtual keys"><div className="modal-heading"><h2>Filter virtual keys</h2><button className="icon-button" aria-label="Close filters" onClick={onClose}>×</button></div><div className="form-card"><label>Organization<select value={draft.organization} onChange={(event) => setDraft({ ...draft, organization: event.target.value, team: "", user: "" })}><option value="">All organizations</option>{organizations.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label><label>Team<select value={draft.team} onChange={(event) => setDraft({ ...draft, team: event.target.value, user: "" })}><option value="">All teams</option>{availableTeams.map((item) => <option key={item.id} value={item.id}>{item.name || item.id}</option>)}</select></label><label>User<select value={draft.user} onChange={(event) => setDraft({ ...draft, user: event.target.value })}><option value="">All users</option>{availableUsers.map((item) => <option key={item.id} value={item.id}>{userLabel(item)}</option>)}</select></label><label>Status<select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}><option value="">All statuses</option>{["active", "disabled", "revoked", "expired"].map((status) => <option value={status} key={status}>{status}</option>)}</select></label><label>Key ID<input value={draft.key} onChange={(event) => setDraft({ ...draft, key: event.target.value })} /></label></div><div className="modal-actions"><button className="secondary" onClick={() => { setDraft(emptyFilter); onChange(emptyFilter); onClose(); }}>Reset filters</button><button onClick={() => { onChange(draft); onClose(); }}>Apply filters</button></div></section></div>;
}

export function VirtualKeysPage() {
  const { client } = useAuth();
  const [keys, setKeys] = useState<VirtualKey[]>([]); const [users, setUsers] = useState<User[]>([]); const [teams, setTeams] = useState<Team[]>([]); const [organizations, setOrganizations] = useState<Organization[]>([]); const [accessGroups, setAccessGroups] = useState<AccessGroup[]>([]); const [models, setModels] = useState<string[]>([]); const [financials, setFinancials] = useState<Record<string, KeyBudgetProjection>>({}); const [usage, setUsage] = useState<KeyUsage[]>([]);
	const [search, setSearch] = useState(""); const [filters, setFilters] = useState(() => ({ ...emptyFilter, key: new URLSearchParams(window.location.search).get("key_id") || "" })); const [filterOpen, setFilterOpen] = useState(false); const [form, setForm] = useState<"create" | VirtualKey>(); const [issued, setIssued] = useState<IssuedKey>(); const [copied, setCopied] = useState(false); const [copyError, setCopyError] = useState("");
	const [editRequested, setEditRequested] = useState(() => new URLSearchParams(window.location.search).get("edit") === "1");
	const [sort, setSort] = useState<SortKey>("created"); const [direction, setDirection] = useState<"asc" | "desc">("desc"); const [loading, setLoading] = useState(true); const [error, setError] = useState("");
  const [page, setPage] = useState(0); const [total, setTotal] = useState(0); const [pageSizeOption, setPageSizeOption] = useState("25"); const [customPageSize, setCustomPageSize] = useState("25");
  const [visibleColumns, setVisibleColumns] = useState(() => new Set(defaultKeyColumns));
	const pageSize = pageSizeOption === "custom" ? Math.min(500, Math.max(1, Number(customPageSize) || 1)) : Number(pageSizeOption);
	const safe = useCallback(async (path: string) => { try { return await client.request(path); } catch { return { data: [] }; } }, [client]);
	const loadReferences = useCallback(async () => {
		const [userPayload, teamPayload, organizationPayload, accessGroupPayload, modelPayload, usagePayload] = await Promise.all([safe("/admin/v1/users?limit=500"), safe("/admin/v1/teams?limit=500"), safe("/admin/v1/organizations?limit=500"), safe("/admin/v1/access-groups"), safe("/v1/models"), safe("/admin/v1/usage/report?days=30")]);
		setUsers(records<User>(userPayload)); setTeams(records<Team>(teamPayload)); setOrganizations(records<Organization>(organizationPayload)); setAccessGroups(records<AccessGroup>(accessGroupPayload)); setUsage(records<KeyUsage>((usagePayload as { by_key?: unknown }).by_key));
		setModels([...new Set(records<Model>(modelPayload).map((model) => model.id || model.model || "").filter(Boolean))].sort());
	}, [safe]);
	const loadKeys = useCallback(async () => {
		setLoading(true); setError("");
		try {
			const query = new URLSearchParams({ limit: String(pageSize), offset: String(page * pageSize), sort_by: sort, sort_order: direction, expand: "financials" });
			if (search.trim()) query.set("search", search.trim());
			if (filters.organization) query.set("organization_id", filters.organization);
			if (filters.team) query.set("team_id", filters.team);
			if (filters.user) query.set("user_id", filters.user);
			if (filters.key.trim()) query.set("key_id", filters.key.trim());
			if (filters.status) query.set("status", filters.status);
			const payload = await client.request<{ data?: VirtualKey[]; total?: number; financials?: Record<string, KeyBudgetProjection> }>(`/admin/v1/keys?${query.toString()}`);
			setKeys(records<VirtualKey>(payload)); setTotal(Number(payload.total || 0)); setFinancials(payload.financials || {});
		} catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load virtual keys"); }
		finally { setLoading(false); }
	}, [client, direction, filters, page, pageSize, search, sort]);
	useEffect(() => { void loadReferences(); }, [loadReferences]);
	useEffect(() => { const timer = window.setTimeout(() => { void loadKeys(); }, search ? 250 : 0); return () => window.clearTimeout(timer); }, [loadKeys, search]);
	useEffect(() => { if (editRequested) { const selected = keys.find((item) => item.id === filters.key); if (selected) { setForm(selected); setEditRequested(false); } } }, [editRequested, filters.key, keys]);
	const rows = keys;
	const usersByID = useMemo(() => new Map(users.map((user) => [user.id, user])), [users]);
	const teamsByID = useMemo(() => new Map(teams.map((team) => [team.id, team])), [teams]);
	const organizationsByID = useMemo(() => new Map(organizations.map((organization) => [organization.id, organization])), [organizations]);
	const pageCount = Math.max(1, Math.ceil(total / pageSize));
	const visibleRows = rows;
	useEffect(() => { if (page >= pageCount) setPage(pageCount - 1); }, [page, pageCount]);
	const load = useCallback(async () => { await Promise.all([loadReferences(), loadKeys()]); }, [loadKeys, loadReferences]);
  function sortBy(next: SortKey) { setPage(0); if (sort === next) setDirection((current) => current === "asc" ? "desc" : "asc"); else { setSort(next); setDirection(next === "created" ? "desc" : "asc"); } }
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
    <div className="key-toolbar"><button onClick={() => setForm("create")}>Create Virtual Key</button><div className="key-toolbar-right"><label className="key-search"><span className="sr-only">Search keys by alias</span><input aria-label="Search keys by alias" placeholder="Search by alias" value={search} onChange={(event) => { setPage(0); setSearch(event.target.value); }} /></label><ColumnsMenu columns={keyColumns} visible={visibleColumns} onChange={setVisibleColumns} /><button className="secondary icon-text-button" onClick={() => setFilterOpen(true)}><FilterIcon />Filter{activeFilters ? ` (${activeFilters})` : ""}</button><button className="secondary icon-only-button" aria-label="Refresh virtual keys" title="Refresh" onClick={() => void load()}><RefreshIcon /></button></div></div>
		{error && <ErrorState message={error} retry={() => void load()} />}{loading ? <LoadingState /> : <><div className="table-card"><div className="table-scroll"><table><thead><tr>{visibleColumns.has("key") && <th>{sortHeading("Key", "key")}</th>}{visibleColumns.has("alias") && <th>Alias</th>}{visibleColumns.has("organization") && <th>Organization</th>}{visibleColumns.has("team") && <th>{sortHeading("Team", "team")}</th>}{visibleColumns.has("user") && <th>{sortHeading("User", "user")}</th>}{visibleColumns.has("created") && <th>{sortHeading("Created", "created")}</th>}{visibleColumns.has("spend") && <th>Spend (30d)</th>}{visibleColumns.has("requests") && <th>Requests (30d)</th>}{visibleColumns.has("tokens") && <th>Tokens (30d)</th>}{visibleColumns.has("budget") && <th title="All identity budget policies are enforced simultaneously; currencies are not combined.">Budgets</th>}{visibleColumns.has("budget_used") && <th>Budget used</th>}{visibleColumns.has("budget_remaining") && <th>Budget remaining</th>}{visibleColumns.has("budget_reset") && <th>Budget reset</th>}{visibleColumns.has("status") && <th>Status</th>}{visibleColumns.has("access_groups") && <th>Access groups</th>}{visibleColumns.has("models") && <th>Models</th>}{visibleColumns.has("description") && <th>Description</th>}{visibleColumns.has("roles") && <th>Roles</th>}{visibleColumns.has("tools") && <th>Tools</th>}{visibleColumns.has("rpm") && <th>RPM</th>}{visibleColumns.has("tpm") && <th>TPM</th>}{visibleColumns.has("tags") && <th>Tags</th>}{visibleColumns.has("expires") && <th>Expires</th>}<th><span className="sr-only">Actions</span></th></tr></thead><tbody>{visibleRows.length ? visibleRows.map((row) => { const projection = financials[row.id]; const keyUsage = usageForKey(row.id, usage); const status = keyStatus(row); const team = teamsByID.get(row.team_id || ""); const user = usersByID.get(row.user_id || ""); const organization = organizationsByID.get(row.organization_id || ""); const actions: ActionMenuItem[] = [{ label: "Inspect", href: `/api-keys/${encodeURIComponent(row.id)}` }, { label: "Edit", onSelect: () => setForm(row) }, { label: "Rotate", onSelect: () => rotate(row) }]; if (!row.revoked_at) actions.push({ label: row.disabled_at ? "Enable" : "Disable", onSelect: () => toggle(row) }); actions.push({ label: "Revoke", tone: "danger", onSelect: () => revoke(row) }); return <tr key={row.id}>{visibleColumns.has("key") && <td><code>{row.id}</code></td>}{visibleColumns.has("alias") && <td><strong>{row.alias || "—"}</strong></td>}{visibleColumns.has("organization") && <td>{row.organization_id ? <><strong>{organization?.name || row.organization_id}</strong>{organization?.name && <><br/><span className="muted">{row.organization_id}</span></>}</> : "—"}</td>}{visibleColumns.has("team") && <td>{row.team_id ? <><strong>{team?.name || row.team_id}</strong>{team?.name && <><br/><span className="muted">{row.team_id}</span></>}</> : "—"}</td>}{visibleColumns.has("user") && <td>{row.user_id ? <><strong>{user?.name || user?.email || row.user_id}</strong>{(user?.name || user?.email) && <><br/><span className="muted">{row.user_id}</span></>}</> : "—"}</td>}{visibleColumns.has("created") && <td>{formatTimestamp(row.created_at)}</td>}{visibleColumns.has("spend") && <td>{keyUsage.spend}</td>}{visibleColumns.has("requests") && <td>{keyUsage.requests.toLocaleString()}</td>}{visibleColumns.has("tokens") && <td>{keyUsage.tokens.toLocaleString()}</td>}{visibleColumns.has("budget") && <td><BudgetCell projection={projection} mode="limit" /></td>}{visibleColumns.has("budget_used") && <td><BudgetCell projection={projection} mode="used" /></td>}{visibleColumns.has("budget_remaining") && <td><BudgetCell projection={projection} mode="remaining" /></td>}{visibleColumns.has("budget_reset") && <td><BudgetCell projection={projection} mode="reset" /></td>}{visibleColumns.has("status") && <td><span className={`status ${status === "active" ? "enabled" : "disabled"}`}>{status}</span></td>}{visibleColumns.has("access_groups") && <td><div className="tag-list">{(row.access_group_ids || []).map((group) => <span className="tag" key={group}>{group}</span>)}</div></td>}{visibleColumns.has("models") && <td><div className="tag-list">{(row.allowed_models || []).map((model) => <span className="tag" key={model}>{model}</span>)}</div></td>}{visibleColumns.has("description") && <td>{row.description || "—"}</td>}{visibleColumns.has("roles") && <td><div className="tag-list">{(row.roles || []).map((role) => <span className="tag" key={role}>{role}</span>)}</div></td>}{visibleColumns.has("tools") && <td><div className="tag-list">{(row.allowed_tools || []).map((tool) => <span className="tag" key={tool}>{tool}</span>)}</div></td>}{visibleColumns.has("rpm") && <td>{row.rate_limit_rpm || "—"}</td>}{visibleColumns.has("tpm") && <td>{row.rate_limit_tpm || "—"}</td>}{visibleColumns.has("tags") && <td><div className="tag-list">{(row.tags || []).map((tag) => <span className="tag" key={tag}>{tag}</span>)}</div></td>}{visibleColumns.has("expires") && <td>{row.expires_at ? formatTimestamp(row.expires_at) : "Never"}</td>}<td className="row-actions"><ActionsMenu label={`Actions for ${row.alias || row.id}`} items={actions} /></td></tr>; }) : <tr><td colSpan={visibleColumns.size + 1}><div className="empty-table">No virtual keys match the current search and filters.</div></td></tr>}</tbody></table></div></div><div className="key-pagination"><div className="key-pagination-summary"><label>Rows per page<select aria-label="Rows per page" value={pageSizeOption} onChange={(event) => { setPage(0); setPageSizeOption(event.target.value); }}>{[10, 25, 50, 100].map((size) => <option value={size} key={size}>{size}</option>)}<option value="custom">Custom</option></select></label>{pageSizeOption === "custom" && <label>Custom rows<input aria-label="Custom rows per page" type="number" min="1" max="500" value={customPageSize} onChange={(event) => { setPage(0); setCustomPageSize(event.target.value); }} /></label>}<span>{total ? `${page * pageSize + 1}–${Math.min(page * pageSize + rows.length, total)} of ${total}` : "0 results"}</span></div><div className="key-pagination-navigation"><button className="secondary" disabled={page === 0} onClick={() => setPage((current) => Math.max(0, current - 1))}>Previous</button><button className="secondary" disabled={page + 1 >= pageCount} onClick={() => setPage((current) => Math.min(pageCount - 1, current + 1))}>Next</button></div></div></>}
    {form && <KeyForm initial={form === "create" ? undefined : form} users={users} teams={teams} organizations={organizations} accessGroups={accessGroups} models={models} onClose={() => setForm(undefined)} onSubmit={saveKey} />}
    {filterOpen && <FilterModal value={filters} organizations={organizations} teams={teams} users={users} onChange={(value) => { setPage(0); setFilters(value); }} onClose={() => setFilterOpen(false)} />}
    {issued && <div className="modal-backdrop" role="presentation"><section className="modal issued-key-modal" role="dialog" aria-modal="true" aria-label="Virtual key created"><div className="modal-heading"><h2>Save this key now</h2><button className="icon-button" aria-label="Close issued key" onClick={() => setIssued(undefined)}>×</button></div><p>This plaintext token is shown exactly once. It cannot be recovered after this window is closed.</p><label>Virtual key<input readOnly value={issued.token} onFocus={(event) => event.currentTarget.select()} /></label>{copyError && <p className="form-error" role="alert">{copyError}</p>}{copied && <div className="copy-confirmation" role="status"><span aria-hidden="true">✓</span> Copied to clipboard</div>}<div className="modal-actions"><button className="secondary" onClick={() => setIssued(undefined)}>Close</button><button onClick={() => void copyToken()}>{copied ? "Copy again" : "Copy"}</button></div></section></div>}
  </>;
}
