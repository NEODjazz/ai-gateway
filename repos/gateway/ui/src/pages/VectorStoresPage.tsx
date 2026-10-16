import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { APIClient } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { ActionsMenu } from "../components/ActionsMenu";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { PageTabs } from "../components/PageTabs";
import { StatCard } from "../components/StatCard";
import { DeleteStoreForm, StoreForm } from "./vector-storage/StoreForm";
import { DocumentsPanel } from "./vector-storage/DocumentsPanel";
import { StoreSearch } from "./vector-storage/StoreSearch";
import { StorageClient, useStorageQuery } from "./vector-storage/runtime";
import { bytes, parseDirectoryPage, parseVectorStore, resourceID, timestamp, type VectorStore } from "./vector-storage/types";

function VectorStoreDirectory({ onCreate }: { onCreate: () => void }) {
  const [search, setSearch] = useState(""), [status, setStatus] = useState(""), [offset, setOffset] = useState(0), [limit, setLimit] = useState(25);
  const [sort, setSort] = useState("created_at"), [order, setOrder] = useState<"asc" | "desc">("desc");
  const [editing, setEditing] = useState<VectorStore>(), [removing, setRemoving] = useState<VectorStore>(), [notice, setNotice] = useState("");
  const path = `/v1/vector_stores/page?${new URLSearchParams({ search, status, offset: String(offset), limit: String(limit), sort_by: sort, sort_order: order })}`;
  const page = useStorageQuery(path, parseDirectoryPage);
  const reset = (change: () => void) => { setOffset(0); change(); };
  const columns = [
    { key: "name", label: "Name", className: "directory-identity-column", render: (value: unknown, row: Record<string, unknown>) => <div><Link to={`?store=${encodeURIComponent(String(row.id))}`}>{String(value)}</Link><small className="directory-summary-detail">{String(row.id)}</small></div> },
    { key: "description", label: "Description", render: (_: unknown, row: Record<string, unknown>) => (row.metadata as Record<string, string>).description || "—" },
    { key: "provider", label: "Provider", render: () => "Gateway-managed" },
    { key: "file_counts", label: "Documents", render: (value: unknown) => String((value as VectorStore["file_counts"]).total) },
    { key: "usage_bytes", label: "Storage", render: (value: unknown) => bytes(Number(value)) }, { key: "status", label: "Status" },
    { key: "created_at", label: "Created", render: (value: unknown) => timestamp(Number(value)) },
    { key: "last_active_at", label: "Last active", render: (value: unknown) => timestamp(Number(value)) },
    { key: "expires_at", label: "Expires", render: (value: unknown) => value === undefined ? "No expiry" : timestamp(Number(value)) },
  ];
  return <>{notice && <p role="status" className="operation-result">{notice}</p>}<label className="vector-status-filter">Status<select value={status} onChange={(event) => reset(() => setStatus(event.target.value))}><option value="">All statuses</option><option value="completed">Completed</option><option value="expired">Expired</option></select></label>{page.error && <ErrorState message={page.error} retry={() => void page.reload()} />}{page.data && (page.loading || page.error || page.previous) && <p role="status">Showing the last successful directory page{page.loading ? " while refreshing" : ""}.</p>}
    {!page.data ? page.loading ? <LoadingState /> : null : <ManagedDataTable rows={page.data?.data || []} columns={columns} defaultHidden={["last_active_at", "expires_at"]} onRefresh={page.reload} primaryAction={<button onClick={onCreate}>Create vector store</button>} searchPlaceholder="Search vector stores by name, ID or description"
      server={{ search, onSearchChange: (value) => reset(() => setSearch(value)), total: page.data?.total || 0, offset: page.data?.offset ?? 0, pageSize: page.data?.limit ?? limit, onOffsetChange: setOffset, onPageSizeChange: (value) => reset(() => setLimit(value)), sort, direction: order, sortableKeys: ["name", "created_at", "last_active_at"], onSortChange: (key, direction) => reset(() => { setSort(key); setOrder(direction); }) }}
      actions={(row) => <ActionsMenu label={`Actions for vector store ${row.id}`} items={[{ label: "Inspect", href: `/vector-stores?store=${encodeURIComponent(String(row.id))}` }, { label: "Edit", disabled: page.loading || Boolean(page.error), onSelect: () => setEditing(row as VectorStore) }, { label: "Copy vector store ID", onSelect: () => { if (!navigator.clipboard) { setNotice("Clipboard is unavailable. Select the visible ID to copy it manually."); return; } void navigator.clipboard.writeText(String(row.id)).then(() => setNotice("Vector store ID copied.")).catch(() => setNotice("Could not copy the ID. Select the visible ID to copy it manually.")); } }, { label: "Delete", disabled: page.loading || Boolean(page.error), onSelect: () => setRemoving(row as VectorStore) }]} />} />}
    {page.data && page.data.total > 0 && !page.data.data.length && <button className="secondary" onClick={() => setOffset(0)}>Return to first page</button>}
    {editing && <StoreForm initial={editing} onClose={() => setEditing(undefined)} onSaved={() => { setEditing(undefined); void page.reload(); setNotice("Vector store saved."); }} />}{removing && <DeleteStoreForm store={removing} onClose={() => setRemoving(undefined)} onDeleted={() => { setRemoving(undefined); void page.reload(); setNotice("Vector store deleted. Source files were retained."); }} />}
  </>;
}

function VectorStoreDetails({ id }: { id: string }) {
  const [parameters, setParameters] = useSearchParams();
  const [editing, setEditing] = useState(false), [removing, setRemoving] = useState(false);
  const parse = useCallback((value: unknown) => { const store = parseVectorStore(value); if (store.id !== id) throw new Error("The server returned another vector store."); return store; }, [id]);
  const data = useStorageQuery(`/v1/vector_stores/${encodeURIComponent(id)}`, parse);
  const store = data.data;
  const requested = parameters.get("tab") || "details", tab = ["details", "documents", "test"].includes(requested) ? requested : "details";
  if (!store) return data.error ? <ErrorState message={data.error} retry={() => void data.reload()} /> : <LoadingState />;
  return <><div className="vector-details-heading"><div><h2>{store.name}</h2><code>{store.id}</code></div><div className="inline-actions"><Link className="button-link secondary" to="?view=manage">Back to vector stores</Link><button className="secondary" disabled={data.loading} onClick={() => void data.reload()}>Refresh store</button><button disabled={data.loading || Boolean(data.error)} onClick={() => setEditing(true)}>Edit store</button><button className="danger" disabled={data.loading || Boolean(data.error)} onClick={() => setRemoving(true)}>Delete store</button></div></div>{data.error && <ErrorState message={data.error} retry={() => void data.reload()} />}{store.status === "expired" && <p role="status">This store has expired. Ingestion and search are unavailable while it remains expired.</p>}
    <div className="usage-stats-grid"><StatCard label="Status" value={store.status} /><StatCard label="Documents" value={store.file_counts.total} /><StatCard label="Stored data" value={bytes(store.usage_bytes)} /><StatCard label="Provider" value="Gateway" /></div>
    <PageTabs label="Vector store workspace" value={tab} items={[{ value: "details", label: "Details" }, { value: "documents", label: "Documents" }, { value: "test", label: "Test vector store" }]} onUpdate={(value) => setParameters({ store: id, tab: value })} />
    {tab === "details" && <section className="form-card"><h2>Store metadata</h2><dl className="detail-grid"><div><dt>Created</dt><dd>{timestamp(store.created_at)}</dd></div><div><dt>Last active</dt><dd>{timestamp(store.last_active_at)}</dd></div><div><dt>Expiry</dt><dd>{store.expires_after ? `${store.expires_after.days} inactive days · ${timestamp(store.expires_at)}` : "No expiry"}</dd></div><div><dt>Description</dt><dd>{store.metadata.description || "—"}</dd></div></dl><pre className="vector-content">{JSON.stringify(store.metadata, null, 2)}</pre><p className="muted">Stored files and chunking settings are durable. Search computes embeddings when requested; Completed does not imply that a persistent external index has been built.</p></section>}
    {tab === "documents" && <DocumentsPanel key={id} storeID={id} expired={store.status === "expired"} onChanged={() => void data.reload()} />}{tab === "test" && <StoreSearch key={id} store={store} />}
    {editing && <StoreForm initial={store} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); void data.reload(); }} />}{removing && <DeleteStoreForm store={store} onClose={() => setRemoving(false)} onDeleted={() => setParameters({ view: "manage" })} />}
  </>;
}

function VectorStorageWorkspace() {
  const [parameters, setParameters] = useSearchParams();
  const id = parameters.get("store"), view = parameters.get("view") || "manage";
  const navigate = (value: string) => setParameters({ view: value });
  if (id && !resourceID(id)) return <><ErrorState message="Invalid vector store ID." /><Link to="?view=manage">Back to vector stores</Link></>;
  if (id) return <VectorStoreDetails key={id} id={id} />;
  return <><PageTabs label="Vector storage" value={["manage", "create", "test"].includes(view) ? view : "manage"} onUpdate={navigate} items={[{ value: "manage", label: "Manage vector stores" }, { value: "create", label: "Create vector store" }, { value: "test", label: "Test vector store" }]} />
    {view === "create" ? <><p>Create the store first, then upload documents or attach existing files in its Documents tab.</p><StoreForm inline onClose={() => navigate("manage")} onSaved={(store) => setParameters({ store: store.id, tab: "documents" })} /></> : view === "test" ? <StoreSearch /> : <VectorStoreDirectory onCreate={() => navigate("create")} />}
  </>;
}

export function VectorStoresPage() {
  const { client, session, token } = useAuth();
  const scope = JSON.stringify([session?.credential_id, session?.user_id, session?.team_id, session?.organization_id, session?.roles]);
  const [source, setSource] = useState("session"), [draft, setDraft] = useState("");
  const [selected, setSelected] = useState<{ scope: string; token: string; sessionToken: string }>();
  useEffect(() => { setSelected(undefined); setDraft(""); setSource("session"); }, [scope, token]);
  const key = source === "key" && selected?.scope === scope && selected.sessionToken === token ? selected.token : "";
  const activeClient = useMemo(() => key ? new APIClient(() => key, { credentials: "omit", sessionEvents: false }) : client, [client, key]);
  const generation = useMemo(() => crypto.randomUUID(), [activeClient, scope]);
  function apply(event: FormEvent) { event.preventDefault(); if (draft.trim()) { setSelected({ scope, token: draft.trim(), sessionToken: token }); setDraft(""); } }
  return <><PageHeader eyebrow="Tools" title="Vector stores" description="Create and manage document collections, inspect attachments and test retrieval using authorized embedding models." />
    <section className="form-card vector-storage-connection" aria-label="Storage credential"><div className="form-grid"><label>Resource credential<select value={source} onChange={(event) => { setSource(event.target.value); setSelected(undefined); setDraft(""); }}><option value="session">Current UI session</option><option value="key">Virtual key</option></select></label>{source === "key" && <form onSubmit={apply}><label>Virtual key<input type="password" autoComplete="off" required value={draft} onChange={(event) => setDraft(event.target.value)} /></label><button disabled={!draft.trim()}>Use virtual key</button></form>}</div><p className="muted">Resources belong to the exact credential and user. An administrator does not see every user's documents. Virtual keys stay in memory and are never included in URLs or saved to browser storage. Switching credentials clears drafts, documents and search results.</p><p>Active: {source === "key" ? key ? "selected virtual key" : "no virtual key selected" : "current UI session"}</p></section>
    {source !== "key" || key ? <StorageClient.Provider value={activeClient}><VectorStorageWorkspace key={generation} /></StorageClient.Provider> : <p>Enter a virtual key to load its vector stores.</p>}
  </>;
}
