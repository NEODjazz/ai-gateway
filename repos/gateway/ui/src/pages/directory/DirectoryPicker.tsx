import { useState } from "react";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { isDirectoryPage, useDirectoryResource, type DirectoryPage } from "./useDirectoryResource";
import type { DirectoryIdentity, IdentityKind } from "./IdentityForm";

export function DirectoryPicker({ kind, label, selected, onChange }: { kind: IdentityKind; label: string; selected?: DirectoryIdentity; onChange: (value: DirectoryIdentity) => void }) {
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const q = new URLSearchParams({ search: search.trim(), status: "active", include_deleted: "false", sort_by: "displayName", sort_order: "ascending", limit: "25", offset: String(offset) });
  const data = useDirectoryResource<DirectoryPage<DirectoryIdentity>>(`/admin/v1/${kind}?${q}`, `picker:${kind}`, isDirectoryPage);
  const choices = data.value?.data || [];
  const options = selected && !choices.some((row) => row.id === selected.id) ? [selected, ...choices] : choices;
  return <div className="directory-picker">
    <label>Search {kind}<input type="search" aria-label={`Search ${kind} to add`} maxLength={256} value={search} onChange={(event) => { setSearch(event.target.value); setOffset(0); }} /></label>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}
    {data.loading && <LoadingState />}
    <label>{label}<select required aria-label={label} value={selected?.id || ""} onChange={(event) => { const row = options.find((item) => item.id === event.target.value); if (row) onChange(row); }}><option value="">Select configured {kind === "users" ? "user" : "team"}</option>{options.map((row) => <option key={row.id} value={row.id}>{row.name || row.email || row.id} · {row.id}</option>)}</select></label>
    <div className="inline-actions"><button type="button" className="secondary" disabled={data.loading || !data.value || data.value.offset === 0} onClick={() => setOffset(Math.max(0, (data.value?.offset || 0) - 25))}>Previous {kind === "users" ? "user" : "team"} choices</button><span>{data.value ? `${data.value.total ? data.value.offset + 1 : 0}–${data.value.offset + choices.length} of ${data.value.total}` : "Choices unavailable"}</span><button type="button" className="secondary" disabled={data.loading || !data.value || data.value.offset + 25 >= data.value.total} onClick={() => setOffset((data.value?.offset || 0) + 25)}>Next {kind === "users" ? "user" : "team"} choices</button></div>
    <p className="muted">Only active identities visible in your session are offered. Existing memberships are not overwritten.</p>
  </div>;
}
