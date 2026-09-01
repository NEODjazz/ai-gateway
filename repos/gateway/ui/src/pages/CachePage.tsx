import { useCallback, useEffect, useMemo, useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { ManagedDataTable } from "../components/ManagedDataTable";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";

type CacheKind = { enabled: boolean; hits: number; misses: number; errors: number; writes: number; hit_ratio: number };
type CacheConfig = { exact_ttl_seconds: number; exact_max_bytes: number; semantic_ttl_seconds: number; semantic_max_entries: number; semantic_max_bytes: number };
type CacheDiagnostics = { config: CacheConfig; exact: CacheKind; semantic: CacheKind; operations: Record<string, Record<string, number>> };

function formatBytes(value: number) {
  if (!value) return "Unlimited / backend-managed";
  const units = ["B", "KiB", "MiB", "GiB"];
  let amount = value; let index = 0;
  while (amount >= 1024 && index < units.length - 1) { amount /= 1024; index++; }
  return `${amount.toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[index]}`;
}

function formatTTL(seconds: number) {
  if (!seconds) return "Disabled";
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} sec`;
}

function CacheKindCard({ title, description, kind, ttl, maxBytes, maxEntries }: { title: string; description: string; kind: CacheKind; ttl: number; maxBytes: number; maxEntries?: number }) {
  return <article className="table-card cache-kind-card"><div className="cache-kind-heading"><div><h2>{title}</h2><p>{description}</p></div><span className={`status ${kind.enabled ? "enabled" : "disabled"}`}>{kind.enabled ? "Enabled" : "Disabled"}</span></div>
    <div className="cache-kind-ratio"><strong>{(kind.hit_ratio * 100).toFixed(1)}%</strong><span>hit ratio</span></div>
    <dl className="detail-grid"><div><dt>Hits</dt><dd>{kind.hits.toLocaleString()}</dd></div><div><dt>Misses</dt><dd>{kind.misses.toLocaleString()}</dd></div><div><dt>Writes</dt><dd>{kind.writes.toLocaleString()}</dd></div><div><dt>Errors</dt><dd>{kind.errors.toLocaleString()}</dd></div><div><dt>TTL</dt><dd>{formatTTL(ttl)}</dd></div><div><dt>Max bytes</dt><dd>{formatBytes(maxBytes)}</dd></div>{maxEntries !== undefined && <div><dt>Max entries</dt><dd>{maxEntries ? maxEntries.toLocaleString() : "Backend-managed"}</dd></div>}</dl>
  </article>;
}

export function CachePage() {
  const { client } = useAuth();
  const [diagnostics, setDiagnostics] = useState<CacheDiagnostics>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    setLoading(true); setError("");
    try { setDiagnostics(await client.request<CacheDiagnostics>("/admin/v1/cache/diagnostics")); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load cache diagnostics"); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(); }, [load]);
  const operations = useMemo(() => Object.entries(diagnostics?.operations || {}).flatMap(([operation, results]) => Object.entries(results).map(([result, count]) => ({ id: `${operation}:${result}`, operation, result, count }))), [diagnostics]);
  if (loading && !diagnostics) return <LoadingState />;
  if (error && !diagnostics) return <ErrorState message={error} retry={() => void load()} />;
  if (!diagnostics) return <ErrorState message="Cache diagnostics are unavailable" retry={() => void load()} />;
  const hits = diagnostics.exact.hits + diagnostics.semantic.hits;
  const misses = diagnostics.exact.misses + diagnostics.semantic.misses;
  const lookups = hits + misses;
  const errors = diagnostics.exact.errors + diagnostics.semantic.errors;
  const writes = diagnostics.exact.writes + diagnostics.semantic.writes;
  return <><PageHeader eyebrow="Performance" title="Caching" description="Current-replica exact and semantic proxy-cache health. Provider prompt-cache tokens remain in Usage and Logs." actions={<button className="secondary" onClick={() => void load()}>Refresh</button>} />
    {error && <ErrorState message={error} retry={() => void load()} />}
    <div className="usage-stats-grid"><StatCard label="Combined hit ratio" value={`${(lookups ? hits / lookups * 100 : 0).toFixed(1)}%`} /><StatCard label="Cache hits" value={hits.toLocaleString()} /><StatCard label="Writes" value={writes.toLocaleString()} /><StatCard label="Errors" value={errors.toLocaleString()} /></div>
    <section className="notice-card cache-boundary"><h2>Two different cache signals</h2><p>This page measures gateway responses served from exact or semantic cache without invoking a provider. Provider-reported cached input tokens are a separate billing signal shown in Usage and request Logs.</p></section>
    <div className="split-grid cache-kind-grid"><CacheKindCard title="Exact cache" description="Credential-scoped deterministic request fingerprint." kind={diagnostics.exact} ttl={diagnostics.config.exact_ttl_seconds} maxBytes={diagnostics.config.exact_max_bytes} /><CacheKindCard title="Semantic cache" description="Opt-in similarity match for constrained text-only requests." kind={diagnostics.semantic} ttl={diagnostics.config.semantic_ttl_seconds} maxBytes={diagnostics.config.semantic_max_bytes} maxEntries={diagnostics.config.semantic_max_entries} /></div>
    <section className="section-block"><h2>Operation counters</h2><p>Bounded process counters by cache operation and result. Runtime configuration is deployment-managed and cannot be changed from the console.</p><ManagedDataTable rows={operations} columns={[{ key: "operation", label: "Operation" }, { key: "result", label: "Result" }, { key: "count", label: "Count" }]} rowKey="id" searchPlaceholder="Search cache operations" onRefresh={load} /></section>
  </>;
}
