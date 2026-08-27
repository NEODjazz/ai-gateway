import { useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { PageHeader } from "../components/PageHeader";

export function PlaygroundPage() {
  const { client } = useAuth();
  const [model, setModel] = useState("");
  const [message, setMessage] = useState("");
  const [result, setResult] = useState("");
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault(); setRunning(true); setError(""); setResult("");
    try {
      const response = await client.request<{ choices?: Array<{ message?: { content?: string } }> }>("/v1/chat/completions", { method: "POST", body: { model, messages: [{ role: "user", content: message }], max_completion_tokens: 256 } });
      setResult(response.choices?.[0]?.message?.content || JSON.stringify(response, null, 2));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Request failed"); }
    finally { setRunning(false); }
  }
  return <><PageHeader eyebrow="Inference" title="Playground" description="Send a request through the same authenticated gateway path." /><div className="split-grid"><form className="form-card" onSubmit={submit}><label>Model<input required value={model} onChange={(event) => setModel(event.target.value)} placeholder="gpt-5.6-luna" /></label><label>Message<textarea required rows={10} value={message} onChange={(event) => setMessage(event.target.value)} /></label>{error && <p role="alert" className="form-error">{error}</p>}<button disabled={running}>{running ? "Running…" : "Run request"}</button></form><section className="output-card"><h2>Model output</h2><pre>{result || "Run a request to see the response."}</pre></section></div></>;
}
