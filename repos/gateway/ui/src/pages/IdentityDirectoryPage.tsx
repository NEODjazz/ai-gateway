import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu, type ActionMenuItem } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { GatewayButton } from "../components/GatewayButton";
import type { Column } from "../components/DataTable";
import { formatTimestamp } from "../format";
import { directoryRoles, IdentityForm, type DirectoryIdentity, type IdentityKind } from "./directory/IdentityForm";
import { BulkUsersForm } from "./directory/BulkUsersForm";
import { useIdentitySummaries } from "./directory/useIdentitySummaries";
import { IdentityResourcesCell, IdentitySummaryCell, type SummaryField } from "./directory/IdentitySummaryCell";

type Page = { data: DirectoryIdentity[]; total: number; offset: number; limit: number; scope: string };
const sorts: Record<string, string> = { id: "id", name: "displayName", email: "userName", external_id: "externalId", status: "status", created_at: "createdAt", updated_at: "updatedAt" };

export function IdentityDirectoryPage({ kind }: { kind: IdentityKind }) {
  const { client, session, hasCapability } = useAuth();
  const admin = hasCapability("admin");
  const scope = JSON.stringify([kind, session?.credential_id, session?.user_id, session?.team_id, session?.organization_id, session?.roles]);
  const [page, setPage] = useState<Page>();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [role, setRole] = useState("");
  const [offset, setOffset] = useState(0);
  const [limit, setLimit] = useState(25);
  const [sort, setSort] = useState("name");
  const [direction, setDirection] = useState<"asc" | "desc">("asc");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState<{ scope: string; text: string; identityID?: string }>();
  const [editing, setEditing] = useState<{ scope: string; value: DirectoryIdentity | null }>();
  const [bulk, setBulk] = useState<{ scope: string }>();
  const activeScope = useRef(scope); activeScope.current = scope;
  const generation = useRef(0);
  const pending = useRef<AbortController | undefined>(undefined);
  const title = kind === "users" ? "Users" : "Teams";
  const validPage = page?.scope === scope ? page : undefined;
  const summaries = useIdentitySummaries(kind === "users" ? "user" : "team", scope, validPage, admin);
  const load = useCallback(async () => {
    const current = ++generation.current;
    pending.current?.abort(); const controller = new AbortController(); pending.current = controller;
    setLoading(true); setError("");
    const q = new URLSearchParams({ offset: String(offset), limit: String(limit), search: search.trim(), status, sort_by: sorts[sort], sort_order: direction === "asc" ? "ascending" : "descending", include_deleted: "false" });
    if (kind === "users" && role) q.set("role", role);
    try {
      const result = await client.request<Omit<Page, "scope">>(`/admin/v1/${kind}?${q}`, { signal: controller.signal });
      if (!Array.isArray(result.data) || !Number.isInteger(result.total) || result.total < 0) throw new Error("Directory returned an invalid page.");
      if (current === generation.current) {
        if (result.total > 0 && offset >= result.total) { setOffset(Math.floor((result.total - 1) / limit) * limit); return; }
        setPage({ ...result, offset, limit, scope });
      }
    } catch (cause) { if (current === generation.current && !controller.signal.aborted) setError(cause instanceof Error ? cause.message : "Could not load directory"); }
    finally { if (current === generation.current) setLoading(false); }
  }, [client, direction, kind, limit, offset, role, scope, search, sort, status]);
  useEffect(() => { void load(); return () => { ++generation.current; pending.current?.abort(); }; }, [load]);
  const columns = useMemo<Column[]>(() => [
    { key: "name", label: kind === "users" ? "User" : "Team", render: (value, row) => <div><Link to={`/${kind}/${encodeURIComponent(String(row.id))}`}>{String(value || row.email || row.id)}</Link>{String(value || row.email || row.id)!==row.id&&<small className="directory-summary-detail">{String(row.id)}</small>}</div> },
    { key: "id", label: "ID" },
    ...(kind === "users" ? [{ key: "email", label: "Email" }, { key: "roles", label: "Approved roles" }, { key: "team_ids", label: "Teams" }] : [{ key: "description", label: "Description" }, { key: "member_count", label: "Members" }]),
    ...(kind === "teams" ? [{ key: "organization", label: "Organization" }, { key: "models", label: "Models & limits" }] : []),
    { key: "resources", label: "Resources" },
    { key: "keys", label: "Virtual keys" }, { key: "spend", label: "Spend (30d)" }, { key: "budgets", label: "Budget usage" },
    { key: "status", label: "Status" }, { key: "external_id", label: "External identity" },
    { key: "created_at", label: "Created", render: (value) => value ? formatTimestamp(String(value)) : "—" },
    { key: "updated_at", label: "Updated", render: (value) => value ? formatTimestamp(String(value)) : "—" }
  ], [kind]);
  const byID = new Map(summaries.value?.data.map((row) => [row.id, row]));
  const summaryFields = new Set(["organization", "models", "keys", "spend", "budgets"]);
  const renderedColumns: Column[] = columns.map((column) => column.key==="resources" ? {...column,className:"directory-summary-column directory-summary-resources",render:(_value,row)=><IdentityResourcesCell summary={byID.get(String(row.id))} kind={kind} id={String(row.id)} admin={admin} loading={summaries.loading} members={row.member_count as number|undefined} teams={row.team_ids as string[]|undefined}/>} : summaryFields.has(column.key) ? { ...column, className: `directory-summary-column directory-summary-${column.key}`, render: (_value, row) => <IdentitySummaryCell field={column.key as SummaryField} summary={byID.get(String(row.id))} kind={kind} id={String(row.id)} admin={admin} loading={summaries.loading} /> } : column);
  async function save(value: DirectoryIdentity) {
    const creating = !editing?.value;
    const { id, ...payload } = value;
    await client.request(`/admin/v1/${kind}${creating ? "" : `/${encodeURIComponent(id)}`}`, { method: creating ? "POST" : "PUT", body: creating ? value : payload });
    if (activeScope.current !== scope) return;
    setNotice({ scope, text: `${kind === "users" ? "User" : "Team"} ${id} ${creating ? "created" : "updated"}.`, identityID:creating?id:undefined });
    await load();
  }
  const resetPage = (update: () => void) => { setOffset(0); setNotice(undefined); update(); };
  return <><PageHeader eyebrow="Access control" title={title} description={kind === "users" ? "Manage directory identities, approved roles and team memberships." : "Manage teams, membership roles and team-owned resources."} />
    {notice?.scope === scope && <div className="operation-result" role="status">{notice.text}{notice.identityID && <> <Link to={`/${kind}/${encodeURIComponent(notice.identityID)}`}>Open workspace</Link></>}</div>}
    {error && <ErrorState message={error} retry={() => void load()} />}
    {summaries.error && <ErrorState message={summaries.error} retry={() => void summaries.reload()} />}
    {admin && summaries.value && <div className="directory-summary-status"><p className="muted">Finalized spend: {formatTimestamp(summaries.value.from)}–{formatTimestamp(summaries.value.to)}. Budget usage includes live reserves in each policy's reset window. Keys include all statuses and member-owned keys in a team. Model policies narrow existing access; zero RPM/TPM adds no identity limit.</p>{summaries.loading && <p role="status">Refreshing summaries… Showing the last successful read.</p>}{summaries.error && <p>Summaries show the previous successful read.</p>}{Object.entries(summaries.value.errors).map(([source, message]) => <p className="form-error" role="alert" key={source}>{message}</p>)}<button className="secondary" onClick={() => void summaries.reload()} disabled={summaries.loading}>Refresh summaries</button></div>}
    <div className="directory-filters"><label>Status<select value={status} onChange={(event) => resetPage(() => setStatus(event.target.value))}><option value="">All statuses</option><option value="active">Active</option><option value="disabled">Disabled</option></select></label>{kind === "users" && <label>Approved role<select value={role} onChange={(event) => resetPage(() => setRole(event.target.value))}><option value="">All roles</option>{directoryRoles.map((value) => <option key={value}>{value}</option>)}</select></label>}<p className="muted">{admin ? "Platform directory" : `Directory scoped to team ${session?.team_id || "—"}`}</p></div>
    {loading && !validPage ? <LoadingState /> : <div aria-busy={loading}>
      {loading && <p role="status">Refreshing directory… Showing the previous page until the new request completes.</p>}
      {!loading && error && validPage && <p className="muted">Showing the previous successful page. The requested changes could not be loaded.</p>}
      <ManagedDataTable key={kind} rows={validPage?.data || []} columns={renderedColumns} defaultHidden={["id", "description", "member_count", "team_ids", "models", "keys", "external_id", "updated_at"]} searchPlaceholder={`Search ${kind} by name${kind === "users" ? ", email" : ""} or ID`} onRefresh={load}
        toolbarExtra={admin && kind === "users" ? <GatewayButton view="normal" onClick={() => setBulk({ scope })}>Bulk create users</GatewayButton> : undefined}
        primaryAction={admin ? <GatewayButton size="l" onClick={() => setEditing({ scope, value: null })}>Create {kind === "users" ? "User" : "Team"}</GatewayButton> : undefined}
        server={{ search, onSearchChange: (value) => resetPage(() => setSearch(value)), total: validPage?.total || 0, offset: validPage?.offset ?? 0, pageSize: validPage?.limit ?? limit, onOffsetChange: setOffset, onPageSizeChange: (value) => resetPage(() => setLimit(value)), sort, direction, sortableKeys: Object.keys(sorts).filter((key) => kind === "users" || key !== "email"), onSortChange: (key, next) => resetPage(() => { setSort(key); setDirection(next); }) }}
        actions={(row) => {
          const items: ActionMenuItem[] = [{ label: "Inspect", href: `/${kind}/${encodeURIComponent(String(row.id))}` }];
          if (admin || kind === "teams") items.push({ label: "Edit", disabled: loading, onSelect: () => setEditing({ scope, value: row as DirectoryIdentity }) });
          return items.length ? <ActionsMenu label={`Actions for ${kind === "users" ? "user" : "team"} ${row.id}`} items={items} /> : null;
        }} />
    </div>}
    {editing?.scope === scope && <IdentityForm kind={kind} initial={editing.value || undefined} onClose={() => setEditing((current) => current === editing ? undefined : current)} onSave={save} />}
    {admin && kind === "users" && bulk?.scope === scope && <BulkUsersForm onClose={() => setBulk((current) => current === bulk ? undefined : current)} onCreated={() => void load()} />}
  </>;
}
