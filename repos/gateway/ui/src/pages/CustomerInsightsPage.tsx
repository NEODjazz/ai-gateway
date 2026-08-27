import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";

export function CustomerInsightsPage() {
  const { client } = useAuth();
  const [scopeType, setScopeType] = useState("team");
  const [scopeID, setScopeID] = useState("");
  const [days, setDays] = useState(30);
  const [payload, setPayload] = useState<unknown>();
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault(); setError(""); setPayload(undefined);
    try { setPayload(await client.request(`/admin/v1/customers/${encodeURIComponent(scopeType)}/${encodeURIComponent(scopeID)}/usage?days=${days}`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not load customer usage"); }
  }
  return <><PageHeader eyebrow="Customer management" title="Customer insights" description="Inspect scoped usage, budgets and rate limits without exposing bearer secrets or content." /><form className="filter-card" onSubmit={submit}><label>Scope<select value={scopeType} onChange={(event) => setScopeType(event.target.value)}><option value="user">User</option><option value="team">Team</option><option value="key">Virtual key</option></select></label><label>Scope ID<input required value={scopeID} onChange={(event) => setScopeID(event.target.value)} /></label><label>Window<select value={days} onChange={(event) => setDays(Number(event.target.value))}><option value={7}>7 days</option><option value={30}>30 days</option><option value={90}>90 days</option></select></label><button>Search</button></form>{error && <ErrorState message={error} />}{payload !== undefined && <section className="json-card"><pre>{JSON.stringify(payload, null, 2)}</pre></section>}</>;
}
