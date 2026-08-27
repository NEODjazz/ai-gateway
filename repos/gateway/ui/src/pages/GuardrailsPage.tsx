import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { ResourcePage } from "../components/ResourcePage";
import { resourceConfigs } from "./resourceConfigs";

export function GuardrailsPage() {
  const { client } = useAuth();
  const [policy, setPolicy] = useState("");
  const [text, setText] = useState("");
  const [result, setResult] = useState<unknown>();
  const [error, setError] = useState("");
  async function check(event: FormEvent) {
    event.preventDefault(); setError("");
    try { setResult(await client.request("/admin/v1/compliance/check", { method: "POST", body: { policy, text } })); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Compliance check failed"); }
  }
  return <><ResourcePage config={resourceConfigs.guardrails} /><section className="section-block"><h2>Compliance playground</h2><p>Only request ID and the text projection are sent to DLP/AV; content is not persisted by the gateway.</p><div className="split-grid"><form className="form-card" onSubmit={check}><label>Policy<input required value={policy} onChange={(event) => setPolicy(event.target.value)} /></label><label>Text projection<textarea required rows={6} value={text} onChange={(event) => setText(event.target.value)} /></label>{error && <p className="form-error" role="alert">{error}</p>}<button>Run compliance check</button></form><section className="json-card"><pre>{result === undefined ? "Run a check to see metadata-only results." : JSON.stringify(result, null, 2)}</pre></section></div></section></>;
}
