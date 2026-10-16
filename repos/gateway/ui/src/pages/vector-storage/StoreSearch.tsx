import { useState, type FormEvent } from "react";
import { ErrorState } from "../../components/AsyncState";
import { ModelControl } from "../playground/ModelControl";
import { object, parseDirectoryPage, parseSearch, resourceID, type SearchResponse, type VectorStore } from "./types";
import { useStorageClient, useStorageQuery, useStorageTask } from "./runtime";

function parseModels(value: unknown): string[] {
  if (!object(value) || !Array.isArray(value.data) || value.data.some((item) => !object(item) || typeof item.id !== "string")) throw new Error("Model discovery returned invalid data.");
  return [...new Set(value.data.map((item) => item.id as string))];
}
function StorePicker({ value, onChange, disabled }: { value?: VectorStore; onChange: (store: VectorStore | undefined) => void; disabled: boolean }) {
  const [search, setSearch] = useState(""), [offset, setOffset] = useState(0);
  const page = useStorageQuery(`/v1/vector_stores/page?${new URLSearchParams({ search, offset: String(offset), limit: "25", status: "completed", sort_by: "name", sort_order: "asc" })}`, parseDirectoryPage);
  const options = [...new Map([...(value ? [value] : []), ...(page.data?.data || [])].map((store) => [store.id, store])).values()];
  return <fieldset className="vector-form-fields" disabled={disabled}><label>Find vector store<input type="search" maxLength={256} value={search} onChange={(event) => { setSearch(event.target.value); setOffset(0); }} /></label>{page.error && <ErrorState message={page.error} retry={() => void page.reload()} />}<label>Vector store<select required value={value?.id || ""} disabled={page.loading || Boolean(page.error) || disabled} onChange={(event) => onChange(options.find((store) => store.id === event.target.value))}><option value="">Select a store</option>{options.map((store) => <option key={store.id} value={store.id}>{store.name} · {store.id}</option>)}</select></label><div className="inline-actions"><button type="button" className="secondary" disabled={page.loading || offset === 0} onClick={() => setOffset(Math.max(0, offset - 25))}>Previous stores</button><span>{page.data ? `${page.data.total} matching stores` : "Loading stores…"}</span><button type="button" className="secondary" disabled={page.loading || !page.data || offset + 25 >= page.data.total} onClick={() => setOffset(offset + 25)}>Next stores</button></div></fieldset>;
}

export function StoreSearch({ store }: { store?: VectorStore }) {
  const client = useStorageClient(), task = useStorageTask();
  const [selected, setSelected] = useState<VectorStore>(), [model, setModel] = useState(""), [query, setQuery] = useState(""), [limit, setLimit] = useState("10"), [filters, setFilters] = useState("");
  const [history, setHistory] = useState<{ id: string; query: string; response: SearchResponse; model: string }[]>([]);
  const models = useStorageQuery("/v1/models", parseModels);
  const currentStore = store || selected;
  function submit(event: FormEvent) {
    event.preventDefault();
    void task.run(async (signal, current) => {
      if (!currentStore || !resourceID(currentStore.id) || !model.trim() || !query.trim()) throw new Error("Select a vector store, an embedding model and a query.");
      const results = Number(limit);
      if (!Number.isInteger(results) || results < 1 || results > 50) throw new Error("Maximum results must be 1–50.");
      if (Array.from(query.trim()).length > 4096) throw new Error("Search query must not exceed 4096 characters.");
      const filter = filters.trim() ? JSON.parse(filters) : undefined;
      if (filter !== undefined && !object(filter)) throw new Error("Filters must be a JSON object.");
      const response = parseSearch(await client.request(`/v1/vector_stores/${encodeURIComponent(currentStore.id)}/search`, { method: "POST", signal, maximumResponseBytes: 2 * 1024 * 1024, body: { model: model.trim(), query: query.trim(), max_num_results: results, ...(filter ? { filters: filter } : {}) } }));
      if (current()) setHistory((value) => [{ id: crypto.randomUUID(), query: query.trim(), response, model: model.trim() }, ...value].slice(0, 5));
    });
  }
  return <section className="vector-search-grid"><form className="form-card" onSubmit={submit}><h2>Test vector store</h2>{!store && <StorePicker value={selected} disabled={task.busy} onChange={(value) => { setSelected(value); setHistory([]); }} />}<fieldset className="vector-form-fields" disabled={task.busy}>
    {store && <p><strong>{store.name}</strong> · <code>{store.id}</code></p>}
    <ModelControl label="Embedding model" value={model} models={models.data || []} onUpdate={setModel} scope={client} disabled={task.busy} />{models.error && <ErrorState message={models.error} retry={() => void models.reload()} />}
    <p className="muted">Use an embeddings-capable model. Model discovery lists authorized IDs; the Gateway checks actual capabilities. Search computes query/document embeddings and consumes the applicable quota and budget.</p>
    <label>Search query<textarea rows={4} required maxLength={4096} value={query} onChange={(event) => setQuery(event.target.value)} /></label><label>Maximum results<input type="number" min="1" max="50" step="1" value={limit} onChange={(event) => setLimit(event.target.value)} /></label>
    <details><summary>Attribute filters</summary><label>Filters JSON<textarea rows={4} value={filters} onChange={(event) => setFilters(event.target.value)} placeholder={'{"type":"eq","key":"region","value":"eu"}'} /></label></details>
    </fieldset><p className="muted">Search supports at most 20 documents, 1 MiB of combined text and 100 chunks per request. A limit error is reported rather than searching a truncated subset.</p>{currentStore?.status === "expired" && <p role="alert">This store has expired.</p>}{task.error && <ErrorState message={task.error} />}<button disabled={task.busy || !currentStore || currentStore.status === "expired" || !model.trim() || !query.trim()}>{task.busy ? "Searching…" : "Run vector search"}</button>
  </form><section className="vector-search-results" aria-label="Vector search results"><div className="inline-actions"><h2>Results</h2>{history.length > 0 && <button className="secondary" disabled={task.busy} onClick={() => setHistory([])}>Clear history</button>}</div><p className="muted">The last five successful queries stay in memory for this credential. Scores are similarity values, not probabilities.</p>{!history.length && <p>No search has completed yet.</p>}{history.map((entry) => <article className="notice-card" key={entry.id}><h3>{entry.query}</h3><p className="muted">{entry.response.data.length} results · {entry.model}</p>{!entry.response.data.length && <p>No matching documents.</p>}{entry.response.data.map((result, index) => <details key={`${result.file_id}:${index}`}><summary>{result.filename} · Score {result.score.toFixed(4)}</summary><p><code>{result.file_id}</code></p>{result.content.map((content, n) => <pre className="vector-content" key={n}>{content.text}</pre>)}<pre className="vector-content">{JSON.stringify(result.attributes, null, 2)}</pre></details>)}</article>)}</section></section>;
}
