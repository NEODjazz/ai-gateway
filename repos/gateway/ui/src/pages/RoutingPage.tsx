import { useEffect, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ErrorState, LoadingState } from "../components/AsyncState";
import { PageHeader } from "../components/PageHeader";

export function RoutingPage() {
  const { client } = useAuth();
  const [diagnostics, setDiagnostics] = useState<unknown>();
  const [simulation, setSimulation] = useState<unknown>();
  const [model, setModel] = useState("");
  const [provider, setProvider] = useState("");
  const [capabilities, setCapabilities] = useState("");
  const [error, setError] = useState("");
  useEffect(() => { client.request("/admin/v1/routing/diagnostics").then(setDiagnostics).catch((cause) => setError(cause instanceof Error ? cause.message : "Could not load diagnostics")); }, [client]);
  async function submit(event: FormEvent) {
    event.preventDefault(); setError("");
    try { setSimulation(await client.request("/admin/v1/routing/simulate", { method: "POST", body: { model, provider, capabilities: capabilities.split(",").map((value) => value.trim()).filter(Boolean) } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not simulate route"); }
  }
  return <><PageHeader eyebrow="Reliability" title="Routing diagnostics" description="Inspect circuit, adaptive routing, admission and cooldown state, then simulate a decision without calling a provider." />{error && <ErrorState message={error} />}<form className="filter-card" onSubmit={submit}><label>Public model<input required value={model} onChange={(event) => setModel(event.target.value)} /></label><label>Provider filter<input value={provider} onChange={(event) => setProvider(event.target.value)} /></label><label>Capabilities<input value={capabilities} onChange={(event) => setCapabilities(event.target.value)} placeholder="chat, tools" /></label><button>Simulate</button></form><div className="split-grid"><section className="json-card"><h2>Runtime state</h2>{diagnostics === undefined ? <LoadingState /> : <pre>{JSON.stringify(diagnostics, null, 2)}</pre>}</section><section className="json-card"><h2>Simulation</h2><pre>{simulation === undefined ? "Run a simulation to inspect candidate ordering." : JSON.stringify(simulation, null, 2)}</pre></section></div></>;
}
