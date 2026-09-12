import { FormEvent, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";

type SearchResult = { title?: string; url: string; snippet?: string; date?: string; last_updated?: string };
type SearchResponse = { object: "search"; results: SearchResult[] };

export function SearchPage() {
  const { client } = useAuth();
  const [model, setModel] = useState("");
  const [query, setQuery] = useState("");
  const [domains, setDomains] = useState("");
  const [country, setCountry] = useState("");
  const [maxResults, setMaxResults] = useState("10");
  const [result, setResult] = useState<SearchResponse>();
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setError(""); setResult(undefined);
    const searchDomains = domains.split(",").map((value) => value.trim()).filter(Boolean);
    if (!model.trim() || !query.trim()) { setError("Model and query are required."); return; }
    if (country && !/^[A-Z]{2}$/.test(country)) { setError("Country must be a two-letter uppercase code."); return; }
    if (searchDomains.length > 20) { setError("Use no more than 20 domains."); return; }
    const resultLimit = Number(maxResults);
    if (!Number.isInteger(resultLimit) || resultLimit < 1 || resultLimit > 20) { setError("Maximum results must be between 1 and 20."); return; }
    setRunning(true);
    try { setResult(await client.request<SearchResponse>("/v1/search", { method: "POST", body: { model: model.trim(), query: query.trim(), max_results: resultLimit, ...(searchDomains.length ? { search_domain_filter: searchDomains } : {}), ...(country ? { country } : {}) } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Search failed"); }
    finally { setRunning(false); }
  }
  return <><PageHeader eyebrow="AI Hub" title="Search" description="Run an authorized search deployment with bounded results and optional domain and country filters." />
    <section className="playground-grid"><form className="form-card" onSubmit={submit}><h2>Search request</h2><label><span>Model or search deployment</span><input aria-label="Search model" required value={model} onChange={(event) => setModel(event.target.value)} /></label><label><span>Query</span><textarea aria-label="Search query" required rows={5} value={query} onChange={(event) => setQuery(event.target.value)} /></label><label><span>Allowed domains</span><input aria-label="Search domains" placeholder="example.com, docs.example.com" value={domains} onChange={(event) => setDomains(event.target.value)} /></label><div className="form-grid"><label><span>Country</span><input aria-label="Search country" maxLength={2} placeholder="US" value={country} onChange={(event) => setCountry(event.target.value.toUpperCase())} /></label><label><span>Maximum results</span><input aria-label="Maximum search results" type="number" min={1} max={20} value={maxResults} onChange={(event) => setMaxResults(event.target.value)} /></label></div>{error && <ErrorState message={error} />}<button disabled={running}>{running ? "Searching…" : "Run search"}</button></form>
    <section><div className="usage-stats-grid"><StatCard label="Results" value={result?.results.length || 0} /><StatCard label="Limit" value={maxResults || "—"} /></div><div className="search-result-list" aria-live="polite">{result ? result.results.length ? result.results.map((item) => <article className="notice-card" key={item.url}><h2>{item.title || item.url}</h2><a href={item.url} target="_blank" rel="noreferrer">{item.url}</a>{item.snippet && <p>{item.snippet}</p>}{(item.date || item.last_updated) && <small className="muted">{item.date || item.last_updated}</small>}</article>) : <p className="muted">No results returned.</p> : <p className="muted">Results will appear here.</p>}</div></section></section>
  </>;
}
