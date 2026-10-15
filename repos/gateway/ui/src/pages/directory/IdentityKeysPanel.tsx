import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../../auth/AuthContext";
import { ActionsMenu } from "../../components/ActionsMenu";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { ManagedDataTable } from "../../components/ManagedDataTable";
import { formatTimestamp } from "../../format";
import { isDirectoryPage, useDirectoryResource, type DirectoryPage } from "./useDirectoryResource";

type Key = { id: string; alias?: string; user_id?: string; status?: string; allowed_models?: string[]; rate_limit_rpm?: number; rate_limit_tpm?: number; created_at: string };
const sortKeys: Record<string, string> = { id: "key", alias: "alias", status: "status", created_at: "created" };

export function IdentityKeysPanel({ kind, id }: { kind: "team" | "user"; id: string }) {
  const { hasCapability } = useAuth();
  const admin = hasCapability("admin");
  const [search, setSearch] = useState(""); const [offset, setOffset] = useState(0); const [limit, setLimit] = useState(25);
  const [sort, setSort] = useState("created_at"); const [direction, setDirection] = useState<"asc" | "desc">("desc");
  const query = new URLSearchParams({ [`${kind}_id`]: id, search: search.trim(), offset: String(offset), limit: String(limit), sort_by: sortKeys[sort], sort_order: direction });
  const validate = useCallback((value: unknown) => isDirectoryPage(value) && (value as DirectoryPage<Key>).data.every((key) => typeof key.id === "string" && Boolean(key.id) && (kind !== "user" || key.user_id === id)), [kind, id]);
  const data = useDirectoryResource<DirectoryPage<Key>>(admin ? `/admin/v1/keys?${query}` : undefined, `keys:${kind}:${id}`, validate);
  useEffect(() => { if (!data.loading && data.value && data.value.offset > 0 && data.value.offset >= data.value.total) setOffset(data.value.total ? Math.floor((data.value.total - 1) / limit) * limit : 0); }, [data.loading, data.value, limit]);
  const reset = (update: () => void) => { setOffset(0); update(); };
  return <section className="section-block"><h2>Virtual keys</h2>{!admin ? <p>Virtual-key inventory is restricted to global administrators.</p> : <>
    <p>Scoped key metadata. Model patterns and RPM/TPM below belong to each key; additional principal, access-group, global and deployment policies can narrow effective access.</p>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}
    {data.loading && !data.value ? <LoadingState /> : <div aria-busy={data.loading}>{data.loading && <p role="status">Refreshing virtual keys…</p>}{data.previous && data.error && <p>Showing the previous successful page.</p>}
      <ManagedDataTable rows={data.value?.data || []} columns={[{ key: "alias", label: "Alias" }, { key: "id", label: "Key" }, { key: "status", label: "Status" }, { key: "allowed_models", label: "Declared models" }, { key: "rate_limit_rpm", label: "Key RPM" }, { key: "rate_limit_tpm", label: "Key TPM" }, { key: "created_at", label: "Created", render: (value) => formatTimestamp(String(value)) }]} onRefresh={data.reload} searchPlaceholder="Search scoped virtual keys" primaryAction={<Link className="button-link secondary" to="/api-keys">Manage virtual keys</Link>}
        server={{ search, onSearchChange: (value) => reset(() => setSearch(value)), total: data.value?.total || 0, offset: data.value?.offset || 0, pageSize: data.value?.limit || limit, onOffsetChange: setOffset, onPageSizeChange: (value) => reset(() => setLimit(value)), sort, direction, sortableKeys: Object.keys(sortKeys), onSortChange: (key, order) => reset(() => { setSort(key); setDirection(order); }) }} actions={(row) => <ActionsMenu label={`Actions for virtual key ${row.id}`} items={[{ label: "Inspect", href: `/api-keys/${encodeURIComponent(String(row.id))}` }]} />} />
    </div>}
  </>}</section>;
}
