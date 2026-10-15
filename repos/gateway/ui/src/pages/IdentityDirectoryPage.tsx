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
  const activeScope = useRef(scope); activeScope.current = scope;
  const generation = useRef(0);
  const pending = useRef<AbortController | undefined>(undefined);
  const title = kind === "users" ? "Users" : "Teams";
  const validPage = page?.scope === scope ? page : undefined;
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
    { key: "name", label: kind === "users" ? "User" : "Team", render: (value, row) => <Link to={`/${kind}/${encodeURIComponent(String(row.id))}`}>{String(value || row.email || row.id)}</Link> },
    { key: "id", label: "ID" },
    ...(kind === "users" ? [{ key: "email", label: "Email" }, { key: "roles", label: "Approved roles" }, { key: "team_ids", label: "Teams" }] : [{ key: "description", label: "Description" }, { key: "member_count", label: "Members" }]),
    { key: "status", label: "Status" }, { key: "external_id", label: "External identity" },
    { key: "created_at", label: "Created", render: (value) => value ? formatTimestamp(String(value)) : "—" },
    { key: "updated_at", label: "Updated", render: (value) => value ? formatTimestamp(String(value)) : "—" }
  ], [kind]);
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
    <div className="directory-filters"><label>Status<select value={status} onChange={(event) => resetPage(() => setStatus(event.target.value))}><option value="">All statuses</option><option value="active">Active</option><option value="disabled">Disabled</option></select></label>{kind === "users" && <label>Approved role<select value={role} onChange={(event) => resetPage(() => setRole(event.target.value))}><option value="">All roles</option>{directoryRoles.map((value) => <option key={value}>{value}</option>)}</select></label>}<p className="muted">{admin ? "Platform directory" : `Directory scoped to team ${session?.team_id || "—"}`}</p></div>
    {loading && !validPage ? <LoadingState /> : <div aria-busy={loading}>
      {loading && <p role="status">Refreshing directory… Showing the previous page until the new request completes.</p>}
      {!loading && error && validPage && <p className="muted">Showing the previous successful page. The requested changes could not be loaded.</p>}
      <ManagedDataTable rows={validPage?.data || []} columns={columns} defaultHidden={["external_id", "updated_at"]} searchPlaceholder={`Search ${kind} by name${kind === "users" ? ", email" : ""} or ID`} onRefresh={load}
        primaryAction={admin ? <GatewayButton size="l" onClick={() => setEditing({ scope, value: null })}>Create {kind === "users" ? "User" : "Team"}</GatewayButton> : undefined}
        server={{ search, onSearchChange: (value) => resetPage(() => setSearch(value)), total: validPage?.total || 0, offset: validPage?.offset ?? 0, pageSize: validPage?.limit ?? limit, onOffsetChange: setOffset, onPageSizeChange: (value) => resetPage(() => setLimit(value)), sort, direction, sortableKeys: Object.keys(sorts).filter((key) => kind === "users" || key !== "email"), onSortChange: (key, next) => resetPage(() => { setSort(key); setDirection(next); }) }}
        actions={(row) => {
          const items: ActionMenuItem[] = [{ label: "Inspect", href: `/${kind}/${encodeURIComponent(String(row.id))}` }];
          if (admin || kind === "teams") items.push({ label: "Edit", disabled: loading, onSelect: () => setEditing({ scope, value: row as DirectoryIdentity }) });
          return items.length ? <ActionsMenu label={`Actions for ${kind === "users" ? "user" : "team"} ${row.id}`} items={items} /> : null;
        }} />
    </div>}
    {editing?.scope === scope && <IdentityForm kind={kind} initial={editing.value || undefined} onClose={() => setEditing((current) => current === editing ? undefined : current)} onSave={save} />}
  </>;
}
