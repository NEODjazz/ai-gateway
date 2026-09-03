import { useEffect, useRef, useState, type FormEvent } from "react";
import type { SSEEvent } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { PageHeader } from "../components/PageHeader";
import { GatewayButton } from "../components/GatewayButton";
import { ToolbarIconButton } from "../components/ToolbarIconButton";

type PlaygroundMode = "chat" | "responses";
type ModelList = { data?: Array<{ id: string }> };
type TranscriptTurn = { id: number; role: "user" | "assistant"; content: string };
type Usage = { prompt_tokens?: number; completion_tokens?: number; input_tokens?: number; output_tokens?: number; total_tokens?: number };
type ChatResponse = { id?: string; model?: string; choices?: Array<{ message?: { content?: unknown; tool_calls?: unknown } }>; usage?: Usage };
type ResponsesResponse = { id?: string; model?: string; output_text?: string; output?: unknown; usage?: Usage };
type RunMetadata = { id?: string; model?: string; usage?: Usage; streamed: boolean };
const maximumTranscriptTurns = 40;
const maximumVisibleEvents = 50;

function sessionID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return `playground-${crypto.randomUUID()}`;
  return `playground-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function textContent(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(textContent).filter(Boolean).join(" ");
  if (!value || typeof value !== "object") return "";
  const record = value as Record<string, unknown>;
  if (typeof record.text === "string") return record.text;
  if (typeof record.content === "string") return record.content;
  return "";
}

function optionalNumber(value: string): number | undefined {
  if (!value.trim()) return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function parseEvent(event: SSEEvent): Record<string, unknown> | undefined {
  if (event.data === "[DONE]") return undefined;
  try { return JSON.parse(event.data) as Record<string, unknown>; }
  catch { throw new Error(`Malformed streaming event: ${event.data.slice(0, 160)}`); }
}

function streamError(event: SSEEvent, payload: Record<string, unknown>) {
  const error = payload.error;
  if (event.event !== "error" && !error) return;
  if (error && typeof error === "object") {
    const message = (error as Record<string, unknown>).message;
    throw new Error(typeof message === "string" ? message : "Streaming request failed");
  }
  throw new Error(typeof error === "string" ? error : "Streaming request failed");
}

function streamText(mode: PlaygroundMode, payload: Record<string, unknown>): string {
  if (mode === "responses") return typeof payload.delta === "string" ? payload.delta : "";
  const choices = payload.choices;
  if (!Array.isArray(choices) || !choices.length || !choices[0] || typeof choices[0] !== "object") return "";
  return textContent((choices[0] as Record<string, unknown>).delta);
}

function completedResponse(payload: Record<string, unknown>): ResponsesResponse | undefined {
  const response = payload.response;
  return response && typeof response === "object" ? response as ResponsesResponse : undefined;
}

export function PlaygroundPage() {
  const { client } = useAuth();
  const [mode, setMode] = useState<PlaygroundMode>("chat");
  const [models, setModels] = useState<string[]>([]);
  const [modelsError, setModelsError] = useState("");
  const [loadingModels, setLoadingModels] = useState(true);
  const [model, setModel] = useState("");
  const [instructions, setInstructions] = useState("");
  const [message, setMessage] = useState("");
  const [maxTokens, setMaxTokens] = useState("256");
  const [temperature, setTemperature] = useState("");
  const [streaming, setStreaming] = useState(true);
  const [transcript, setTranscript] = useState<TranscriptTurn[]>([]);
  const [pendingOutput, setPendingOutput] = useState("");
  const [metadata, setMetadata] = useState<RunMetadata>();
  const [events, setEvents] = useState<SSEEvent[]>([]);
  const [eventCount, setEventCount] = useState(0);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);
  const [activeSessionID, setActiveSessionID] = useState(sessionID);
  const [previousResponseID, setPreviousResponseID] = useState("");
  const abortRef = useRef<AbortController | undefined>(undefined);
  const turnID = useRef(0);

  async function loadModels() {
    setLoadingModels(true); setModelsError("");
    try {
      const payload = await client.request<ModelList>("/v1/models");
      const available = [...new Set((payload.data || []).map((item) => item.id.trim()).filter(Boolean))].sort();
      setModels(available);
      setModel((current) => current || available[0] || "");
    } catch (cause) { setModelsError(cause instanceof Error ? cause.message : "Could not load models"); }
    finally { setLoadingModels(false); }
  }

  useEffect(() => { void loadModels(); }, [client]);
  useEffect(() => () => { abortRef.current?.abort(); }, []);

  function newSession() {
    abortRef.current?.abort();
    setTranscript([]); setPendingOutput(""); setMetadata(undefined); setEvents([]); setEventCount(0); setError("");
    setPreviousResponseID(""); setActiveSessionID(sessionID());
    turnID.current = 0;
  }

  function changeMode(next: PlaygroundMode) {
    if (next === mode || running) return;
    setMode(next); newSession();
  }

  function requestSettings() {
    return { limit: optionalNumber(maxTokens), heat: optionalNumber(temperature) };
  }

  function chatBody(input: string, stream: boolean) {
    const { limit, heat } = requestSettings();
    const messages = [
      ...(instructions.trim() ? [{ role: "system", content: instructions.trim() }] : []),
      ...transcript.map((turn) => ({ role: turn.role, content: turn.content })),
      { role: "user", content: input }
    ];
    return { model, messages, stream, ...(limit === undefined ? {} : { max_completion_tokens: limit }), ...(heat === undefined ? {} : { temperature: heat }) };
  }

  function responsesBody(input: string, stream: boolean) {
    const { limit, heat } = requestSettings();
    return {
      model, input, stream,
      ...(instructions.trim() ? { instructions: instructions.trim() } : {}),
      ...(previousResponseID ? { previous_response_id: previousResponseID } : {}),
      ...(limit === undefined ? {} : { max_output_tokens: limit }),
      ...(heat === undefined ? {} : { temperature: heat })
    };
  }

  async function runStream(input: string, controller: AbortController): Promise<{ output: string; metadata: RunMetadata; events: SSEEvent[]; eventCount: number }> {
    const path = mode === "chat" ? "/v1/chat/completions" : "/v1/responses";
    const body = mode === "chat" ? chatBody(input, true) : responsesBody(input, true);
    let output = "";
    let responseMetadata: RunMetadata = { streamed: true, model };
    const received: SSEEvent[] = [];
    let receivedCount = 0;
    const streamResult = await client.stream<ChatResponse | ResponsesResponse>(path, { method: "POST", headers: { "X-Session-ID": activeSessionID }, body, signal: controller.signal }, (streamEvent) => {
      if (streamEvent.data === "[DONE]") return;
      receivedCount++;
      received.push(streamEvent);
      if (received.length > maximumVisibleEvents) received.shift();
      const payload = parseEvent(streamEvent);
      if (!payload) return;
      streamError(streamEvent, payload);
      output += streamText(mode, payload);
      if (mode === "responses") {
        const completed = completedResponse(payload);
        if (completed) {
          if (!output) output = completed.output_text || textContent(completed.output);
          responseMetadata = { id: completed.id, model: completed.model || model, usage: completed.usage, streamed: true };
        }
      } else {
        if (typeof payload.id === "string") responseMetadata.id = payload.id;
        if (typeof payload.model === "string") responseMetadata.model = payload.model;
        if (payload.usage && typeof payload.usage === "object") responseMetadata.usage = payload.usage as Usage;
      }
      setPendingOutput(output);
      setEvents([...received]);
      setEventCount(receivedCount);
    }, true);
    if (!streamResult.streamed) {
      if (mode === "chat") {
        const response = streamResult.data as ChatResponse;
        const responseMessage = response.choices?.[0]?.message;
        return {
          output: textContent(responseMessage?.content) || JSON.stringify(responseMessage?.tool_calls || response, null, 2),
          metadata: { id: response.id, model: response.model || model, usage: response.usage, streamed: false },
          events: [], eventCount: 0
        };
      }
      const response = streamResult.data as ResponsesResponse;
      return {
        output: response.output_text || textContent(response.output) || JSON.stringify(response.output || response, null, 2),
        metadata: { id: response.id, model: response.model || model, usage: response.usage, streamed: false },
        events: [], eventCount: 0
      };
    }
    return { output: output || "No text output was returned. Inspect the bounded stream events below.", metadata: responseMetadata, events: received, eventCount: receivedCount };
  }

  async function runJSON(input: string, controller: AbortController): Promise<{ output: string; metadata: RunMetadata }> {
    const options = { method: "POST", headers: { "X-Session-ID": activeSessionID }, signal: controller.signal };
    if (mode === "chat") {
      const response = await client.request<ChatResponse>("/v1/chat/completions", { ...options, body: chatBody(input, false) });
      const responseMessage = response.choices?.[0]?.message;
      return { output: textContent(responseMessage?.content) || JSON.stringify(responseMessage?.tool_calls || response, null, 2), metadata: { id: response.id, model: response.model || model, usage: response.usage, streamed: false } };
    }
    const response = await client.request<ResponsesResponse>("/v1/responses", { ...options, body: responsesBody(input, false) });
    setPreviousResponseID(response.id || "");
    return { output: response.output_text || textContent(response.output) || JSON.stringify(response.output || response, null, 2), metadata: { id: response.id, model: response.model || model, usage: response.usage, streamed: false } };
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const input = message.trim();
    if (!model.trim() || !input) return;
    const controller = new AbortController();
    abortRef.current = controller;
    setRunning(true); setError(""); setPendingOutput(""); setMetadata(undefined); setEvents([]); setEventCount(0);
    try {
      const result = streaming ? await runStream(input, controller) : await runJSON(input, controller);
      if (mode === "responses" && result.metadata.id) setPreviousResponseID(result.metadata.id);
      const userTurn: TranscriptTurn = { id: ++turnID.current, role: "user", content: input };
      const assistantTurn: TranscriptTurn = { id: ++turnID.current, role: "assistant", content: result.output };
      setTranscript((current) => [...current, userTurn, assistantTurn].slice(-maximumTranscriptTurns));
      setPendingOutput(""); setMetadata(result.metadata); setMessage("");
      if ("events" in result && Array.isArray(result.events)) setEvents(result.events as SSEEvent[]);
      if ("eventCount" in result && typeof result.eventCount === "number") setEventCount(result.eventCount);
    } catch (cause) {
      const aborted = cause && typeof cause === "object" && "name" in cause && cause.name === "AbortError";
      setError(aborted ? "Request cancelled" : cause instanceof Error ? cause.message : "Request failed");
    } finally {
      if (abortRef.current === controller) abortRef.current = undefined;
      setRunning(false);
    }
  }

  const usage = metadata?.usage;
  const calculatedTokens = (usage?.prompt_tokens || usage?.input_tokens || 0) + (usage?.completion_tokens || usage?.output_tokens || 0);
  const tokenCount = usage?.total_tokens ?? (calculatedTokens || undefined);
  return <>
    <PageHeader eyebrow="Inference" title="Playground" description="Test models and deployments with production-compatible request settings." />
    <div className="playground-selection-bar"><label>Model<div className="playground-model-control"><input aria-label="Model" disabled={running} required list="playground-models" value={model} onChange={(event) => { setModel(event.target.value); setPreviousResponseID(""); }} placeholder={loadingModels ? "Loading models…" : "Select or enter a logical model"} /><ToolbarIconButton icon="refresh" label="Refresh models" disabled={loadingModels} onClick={() => void loadModels()} /></div><span className="playground-model-help">{models.length ? `${models.length.toLocaleString()} authorized models` : "Manual model entry is available"}</span></label><datalist id="playground-models">{models.map((item) => <option key={item} value={item} />)}</datalist><label>Deployment<select aria-label="Deployment" disabled><option>Automatic gateway routing</option></select><span className="playground-model-help">Resolved by routing policy</span></label><GatewayButton view="outlined" size="l" disabled={running} onClick={newSession}>Clear</GatewayButton></div>
    {modelsError && <p className="form-error" role="status">Model discovery: {modelsError}</p>}
    <div className="playground-workspace">
      <form className="playground-conversation-card" onSubmit={submit}>
        <label>System instructions<textarea aria-label="Instructions" disabled={running} rows={3} value={instructions} onChange={(event) => setInstructions(event.target.value)} placeholder={mode === "chat" ? "Optional system message" : "Optional Responses instructions"} /></label>
        <div className="playground-output-heading"><div><h2>Conversation</h2><span className="muted">Session <code>{activeSessionID}</code></span></div></div>
        <section className="playground-output" aria-label="Playground conversation">{!transcript.length && !pendingOutput && <p className="playground-empty">Run a request to start an in-memory conversation. Prompt and response content is not persisted by the console.</p>}<div className="playground-transcript">{transcript.map((turn) => <article className={`playground-turn ${turn.role}`} key={turn.id}><strong>{turn.role === "user" ? "User" : "Assistant"}</strong><pre>{turn.content}</pre></article>)}{pendingOutput && <article className="playground-turn assistant streaming"><strong>Assistant <span>streaming</span></strong><pre>{pendingOutput}</pre></article>}</div></section>
        <div className="playground-composer"><textarea aria-label="Message" disabled={running} required rows={4} value={message} onChange={(event) => setMessage(event.target.value)} placeholder="Ask the model…" /><GatewayButton size="l" type="submit" disabled={running || !model.trim() || !message.trim()}>{running ? "Running…" : transcript.length ? "Send message" : "Run request"}</GatewayButton>{running && <GatewayButton type="button" view="outlined" size="l" onClick={() => abortRef.current?.abort()}>Stop</GatewayButton>}</div>
        {error && <p role="alert" className="form-error">{error}</p>}
      </form>
      <aside className="playground-side-panel">
        <section className="playground-parameters-card"><h2>Parameters</h2><div className="page-tabs playground-api-tabs" role="tablist" aria-label="Playground API"><button type="button" role="tab" aria-selected={mode === "chat"} className={mode === "chat" ? "active" : ""} onClick={() => changeMode("chat")}>Chat Completions</button><button type="button" role="tab" aria-selected={mode === "responses"} className={mode === "responses" ? "active" : ""} onClick={() => changeMode("responses")}>Responses API</button></div><label>Response format<select aria-label="Response format" disabled><option>Text</option></select></label><label>Temperature<input aria-label="Temperature" disabled={running} type="number" min="0" max="2" step="0.1" value={temperature} onChange={(event) => setTemperature(event.target.value)} placeholder="Provider default" /></label><label>Maximum output tokens<input aria-label="Maximum output tokens" disabled={running} type="number" min="1" step="1" value={maxTokens} onChange={(event) => setMaxTokens(event.target.value)} placeholder="Provider default" /></label><label className="checkbox-line"><input aria-label="Stream response" disabled={running} type="checkbox" checked={streaming} onChange={(event) => setStreaming(event.target.checked)} /> Streaming: {streaming ? "On" : "Off"}</label><p className="muted playground-default-note">Leave optional settings blank to use provider defaults for models that restrict custom sampling values.</p></section>
        <section className="playground-metadata-card"><h2>Response metadata</h2><dl className="playground-metadata"><div><dt>API</dt><dd>{mode === "chat" ? "Chat Completions" : "Responses"}{metadata?.streamed ? " · streamed" : ""}</dd></div><div><dt>Deployment</dt><dd>Gateway-selected</dd></div><div><dt>Upstream model</dt><dd>{metadata?.model || model || "—"}</dd></div><div><dt>Response ID</dt><dd>{metadata?.id || "—"}</dd></div><div><dt>Tokens</dt><dd>{tokenCount ?? "—"}</dd></div></dl>{events.length > 0 && <details className="playground-events"><summary>Stream events ({events.length}{eventCount > events.length ? "+" : ""})</summary><pre>{events.map((item) => `${item.event}: ${item.data}`).join("\n\n")}</pre></details>}</section>
      </aside>
    </div>
  </>;
}
