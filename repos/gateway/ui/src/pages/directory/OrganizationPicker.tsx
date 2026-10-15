import { useState } from "react";
import { ErrorState, LoadingState } from "../../components/AsyncState";
import { isDirectoryPage, useDirectoryResource, type DirectoryPage } from "./useDirectoryResource";

export type OrganizationMetadata = { id: string; name: string; status: "active" | "disabled"; description?: string };
export function isOrganization(value: unknown): value is OrganizationMetadata {
  const row = value as OrganizationMetadata | undefined;
  return Boolean(row && typeof row.id === "string" && row.id && typeof row.name === "string" && ["active", "disabled"].includes(row.status));
}
function isOrganizationPage(value: unknown) { return isDirectoryPage(value) && (value as DirectoryPage<OrganizationMetadata>).limit <= 100 && (value as DirectoryPage<OrganizationMetadata>).data.every((row) => isOrganization(row) && row.status === "active"); }

export function OrganizationPicker({ selected, onChange }: { selected?: OrganizationMetadata; onChange: (organization: OrganizationMetadata) => void }) {
  const [search, setSearch] = useState(""); const [offset, setOffset] = useState(0);
  const query = new URLSearchParams({ search: search.trim(), status: "active", offset: String(offset), limit: "25" });
  const data = useDirectoryResource<DirectoryPage<OrganizationMetadata>>(`/admin/v1/organizations/page?${query}`, "organization-picker", isOrganizationPage);
  const choices = data.value?.data || [];
  const options = selected && !choices.some((row) => row.id === selected.id) ? [selected, ...choices] : choices;
  return <div className="directory-picker">
    <label>Search organizations<input type="search" aria-label="Search organizations to assign" maxLength={256} value={search} onChange={(event) => { setSearch(event.target.value); setOffset(0); }} /></label>
    {data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}{data.loading && <LoadingState />}
    <label>Organization<select aria-label="Team organization" required value={selected?.id || ""} onChange={(event) => { const row = options.find((option) => option.id === event.target.value); if (row) onChange(row); }}><option value="">Select active organization</option>{options.map((row) => <option key={row.id} value={row.id}>{row.name} · {row.id}</option>)}</select></label>
    <div className="inline-actions"><button type="button" className="secondary" disabled={data.loading || !data.value || data.value.offset === 0} onClick={() => setOffset(Math.max(0, (data.value?.offset || 0) - 25))}>Previous organization choices</button><span>{data.value ? `${data.value.total ? data.value.offset + 1 : 0}–${data.value.offset + choices.length} of ${data.value.total}` : "Choices unavailable"}</span><button type="button" className="secondary" disabled={data.loading || !data.value || data.value.offset + 25 >= data.value.total} onClick={() => setOffset((data.value?.offset || 0) + 25)}>Next organization choices</button></div>
    <p className="muted">Choose an active organization. Existing team ownership is never replaced implicitly.</p>
  </div>;
}
