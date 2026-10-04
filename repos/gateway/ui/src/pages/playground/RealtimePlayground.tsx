import { MarkdownOutput } from "./MarkdownOutput";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { GatewayButton } from "../../components/GatewayButton";
import { ModalFrame } from "../../components/ModalFrame";
import { PageTabs } from "../../components/PageTabs";
import { ModelControl } from "./ModelControl";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { CopyOutput } from "./OutputDetails";
import type { PlaygroundConnection } from "./requests";
import { audioBase64, audioBytes, decodeRealtimeEvent, defaultRealtimeSettings, maximumAudioBytes, maximumRealtimeTextBytes, openRealtimeSocket, pcmWAV, realtimeEventSummary, realtimeResponse, realtimeSession, type RealtimeSettings } from "./realtime";
import { startRealtimeRecorder, type RealtimeRecorder } from "./realtimeRecorder";
import { realtimeCode } from "./realtimeCode";

type Turn = { id: number; role: "user" | "assistant"; text: string; status?: string; audio?: { url: string; bytes: number } };
type Pending = { responseID?: string; start: number; firstTokenMS?: number; text: string; chunks: Uint8Array[]; audioBytes: number; cancelRequested: boolean };
type Metrics = { status: string; latencyMS: number; firstTokenMS?: number; inputTokens?: number; outputTokens?: number; audioInputTokens?: number; audioOutputTokens?: number };
const record = (value: unknown): Record<string, unknown> | undefined => value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
const token = (value: unknown): number | undefined => typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
function responseText(output: unknown): string {
  if (!Array.isArray(output)) return "";
  return output.flatMap((item) => { const content = record(item)?.content; return Array.isArray(content) ? content.map((part) => { const block = record(part); return typeof block?.text === "string" ? block.text : typeof block?.transcript === "string" ? block.transcript : typeof block?.refusal === "string" ? block.refusal : ""; }) : []; }).filter(Boolean).join("\n");
}
export function RealtimePlayground({ connection, connectionChanged, connectionControls, models }: { connection: PlaygroundConnection; connectionChanged: boolean; connectionControls: ReactNode; models: string[] }) {
  const [model, setModel] = useState(models[0] || ""), [settings, setSettings] = useState<RealtimeSettings>(defaultRealtimeSettings);
  const [status, setStatus] = useState<"disconnected" | "connecting" | "configuring" | "connected">("disconnected");
  const [input, setInput] = useState(""), [error, setError] = useState(""), [turns, setTurns] = useState<Turn[]>([]), [dropped, setDropped] = useState(0);
  const [output, setOutput] = useState(""), [responding, setResponding] = useState(false), [recording, setRecording] = useState<"idle" | "starting" | "recording" | "finishing">("idle");
  const [metadata, setMetadata] = useState<Metrics>(), [events, setEvents] = useState<string[]>([]), [eventCount, setEventCount] = useState(0);
  const [code, setCode] = useState<{ javascript: string; python: string }>(), [codeLanguage, setCodeLanguage] = useState<"javascript" | "python">("javascript");
  const socket = useRef<WebSocket | undefined>(undefined), controller = useRef<AbortController | undefined>(undefined), micController = useRef<AbortController | undefined>(undefined), recorder = useRef<RealtimeRecorder | undefined>(undefined);
  const pending = useRef<Pending | undefined>(undefined), history = useRef<Turn[]>([]), generation = useRef(0), turnID = useRef(0), inputAudioBytes = useRef(0);
  const permissionAttempt = useRef<Promise<RealtimeRecorder> | undefined>(undefined);
  const configure = useRef<RealtimeSettings>(defaultRealtimeSettings), ready = useRef(false), micPending = useRef(false), micFinishing = useRef(false);
  function stopMicrophone() {
    micController.current?.abort(); micController.current = undefined;
    if (recorder.current) void recorder.current.stop(false).catch(() => {});
    recorder.current = undefined; micPending.current = false; micFinishing.current = false; inputAudioBytes.current = 0;
  }
  function releaseConnection() {
    generation.current++; controller.current?.abort(); controller.current = undefined; stopMicrophone(); ready.current = false;
    const current = socket.current; socket.current = undefined;
    if (current) { current.onopen = null; current.onmessage = null; current.onerror = null; current.onclose = null; current.close(); }
    pending.current = undefined;
  }
  function clearHistory() {
    history.current.forEach((turn) => { if (turn.audio) URL.revokeObjectURL(turn.audio.url); }); history.current = [];
    setTurns([]); setDropped(0); setOutput(""); setEvents([]); setEventCount(0); setMetadata(undefined); setCode(undefined);
  }
  function disconnect(clear = false) {
    const active = pending.current, interrupted = !!active;
    if (!clear && active && (active.text || active.chunks.length)) {
      const bytes = active.chunks.reduce((sum, chunk) => sum + chunk.length, 0);
      const audio = bytes ? { url: URL.createObjectURL(pcmWAV(active.chunks)), bytes } : undefined;
      append({ role: "assistant", text: active.text.slice(0, maximumRealtimeTextBytes) || "Partial audio response", status: "interrupted", audio });
    }
    releaseConnection(); setStatus("disconnected"); setRecording("idle"); setResponding(false); setOutput("");
    if (interrupted) setError("Disconnected before response completion. Provider execution may already have incurred usage; inspect Usage & spend before repeating.");
    if (clear) { clearHistory(); setInput(""); setError(""); }
  }
  useEffect(() => { disconnect(true); setModel(models[0] || ""); return () => { releaseConnection(); history.current.forEach((turn) => { if (turn.audio) URL.revokeObjectURL(turn.audio.url); }); history.current = []; }; }, [connection]);
  useEffect(() => { if (connectionChanged) disconnect(); }, [connectionChanged]);
  useEffect(() => { if (status === "disconnected" && models.length && !model) setModel(models[0]); }, [models]);
  function append(turn: Omit<Turn, "id">) {
    const next = [...history.current, { ...turn, id: ++turnID.current }]; let removed = 0;
    const tooLarge = () => next.length > 40 || next.reduce((sum, item) => sum + new TextEncoder().encode(item.text).length, 0) > maximumRealtimeTextBytes || next.reduce((sum, item) => sum + (item.audio?.bytes || 0), 0) > 16 * 1024 * 1024;
    while (next.length && tooLarge()) { const first = next.shift(); if (first?.audio) URL.revokeObjectURL(first.audio.url); removed++; }
    history.current = next; setTurns(next); setDropped((value) => value + removed);
  }
  function send(event: Record<string, unknown>) {
    const current = socket.current;
    if (!current || current.readyState !== WebSocket.OPEN) throw new Error("Realtime connection is not open.");
    if (current.bufferedAmount > 1024 * 1024) throw new Error("Realtime send buffer exceeded 1 MiB. Reconnect before retrying.");
    current.send(JSON.stringify({ event_id: `playground-${crypto.randomUUID()}`, ...event }));
  }
  function fail(message: string) {
    disconnect(); setError(message);
  }
  function receive(event: Record<string, unknown>) {
    setEventCount((count) => count + 1); setEvents((items) => [...items, realtimeEventSummary(event)].slice(-50));
    if (event.type === "error") {
      const message = record(event.error)?.message;
      fail(typeof message === "string" ? message.slice(0, 8192) : "Realtime provider rejected the event."); return;
    }
    if (event.type === "session.updated") {
      if (ready.current) return;
      ready.current = true; setStatus("connected"); return;
    }
    const active = pending.current;
    if (!active) return;
    const response = record(event.response);
    if (event.type === "response.created") {
      if (active.responseID || typeof response?.id !== "string" || !response.id) throw new Error("Realtime returned an unexpected response identity.");
      active.responseID = response.id;
      if (active.cancelRequested) send({ type: "response.cancel", response_id: active.responseID });
    }
    if (typeof event.response_id === "string" && active.responseID && event.response_id !== active.responseID) throw new Error("Realtime event belongs to another response.");
    const type = String(event.type);
    if (["response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta", "response.audio.delta", "response.output_audio.delta"].includes(type) && (!active.responseID || event.response_id !== active.responseID)) throw new Error("Realtime output is missing its active response identity.");
    if (["response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta"].includes(type)) {
      if (typeof event.delta !== "string") throw new Error("Realtime text delta is invalid.");
      active.text += event.delta;
      if (new TextEncoder().encode(active.text).length > maximumRealtimeTextBytes) throw new Error("Realtime response text exceeded 2 MiB.");
      if (active.firstTokenMS === undefined && event.delta.length) active.firstTokenMS = performance.now() - active.start;
      setOutput(active.text);
    }
    if (["response.audio.delta", "response.output_audio.delta"].includes(type)) {
      if (configure.current.mode !== "voice") throw new Error("Realtime returned unrequested audio.");
      const bytes = audioBytes(event.delta);
      active.audioBytes += bytes.length;
      if (active.audioBytes > maximumAudioBytes) throw new Error("Realtime response audio exceeded 8 MiB.");
      active.chunks.push(bytes);
      if (active.firstTokenMS === undefined) active.firstTokenMS = performance.now() - active.start;
    }
    if (event.type === "response.done") {
      if (!response || typeof response.id !== "string" || !active.responseID || response.id !== active.responseID || !["completed", "cancelled", "failed", "incomplete"].includes(String(response.status))) throw new Error("Realtime completion is missing or mismatches the active response.");
      const finalText = responseText(response.output) || active.text;
      if (new TextEncoder().encode(finalText).length > maximumRealtimeTextBytes) throw new Error("Realtime response text exceeded 2 MiB.");
      const audio = active.audioBytes ? { url: URL.createObjectURL(pcmWAV(active.chunks)), bytes: active.audioBytes } : undefined;
      append({ role: "assistant", text: finalText || (audio ? "Audio response" : "No text output"), status: String(response.status), audio });
      const usage = record(response.usage), inputDetails = record(usage?.input_token_details), outputDetails = record(usage?.output_token_details);
      setMetadata({ status: String(response.status), latencyMS: performance.now() - active.start, firstTokenMS: active.firstTokenMS, inputTokens: token(usage?.input_tokens), outputTokens: token(usage?.output_tokens), audioInputTokens: token(inputDetails?.audio_tokens), audioOutputTokens: token(outputDetails?.audio_tokens) });
      pending.current = undefined; setResponding(false); setOutput("");
      if (response.status !== "completed") setError(`Realtime response ${String(response.status)}. ${String(record(record(response.status_details)?.error)?.message || "Partial output is retained; inspect finalized usage before retrying.")}`);
    }
  }
  async function connect() {
    if (controller.current || socket.current || connectionChanged) return;
    const current = ++generation.current, abort = new AbortController(); controller.current = abort; setError(""); setStatus("connecting"); clearHistory();
    try {
      const session = realtimeSession(settings); configure.current = { ...settings };
      const ws = await openRealtimeSocket(connection, model, abort.signal, window.location.origin, settings.dialect);
      if (abort.signal.aborted || current !== generation.current) { ws.close(); return; }
      socket.current = ws;
      const timeout = setTimeout(() => { if (current === generation.current && !ready.current) fail("Realtime connection did not become ready within 15 seconds."); }, 15000);
      abort.signal.addEventListener("abort", () => clearTimeout(timeout), { once: true });
      ws.onopen = () => { if (current !== generation.current) return; setStatus("configuring"); try { send(session); } catch (cause) { fail(cause instanceof Error ? cause.message : "Could not configure Realtime."); } };
      ws.onmessage = (message) => { if (current !== generation.current) return; try { const event = decodeRealtimeEvent(message.data); receive(event); if (ready.current) clearTimeout(timeout); } catch (cause) { fail(cause instanceof Error ? cause.message : "Invalid Realtime event."); } };
      ws.onerror = () => { if (current === generation.current) fail("Could not open Realtime. Check model/deployment support, encryption configuration and gateway diagnostics."); };
      ws.onclose = (event) => { if (current !== generation.current) return; const interrupted = !!pending.current; disconnect(); setError(interrupted ? "Realtime closed before response completion. Finalized usage may already exist." : event.wasClean ? "Realtime connection closed." : "Realtime connection failed. Check gateway and provider diagnostics."); };
    } catch (cause) { if (current === generation.current) { disconnect(); setError(cause instanceof Error ? cause.message : "Could not connect Realtime."); } }
  }
  function beginResponse() {
    pending.current = { start: performance.now(), text: "", chunks: [], audioBytes: 0, cancelRequested: false };
    setResponding(true); setMetadata(undefined); setOutput("");
    send(realtimeResponse(configure.current));
  }
  function submit(event: FormEvent) {
    event.preventDefault(); if (!ready.current || pending.current || micPending.current || connectionChanged) return;
    const text = input.trim(); if (!text) return;
    try {
      if (new TextEncoder().encode(text).length > 1024 * 1024) throw new Error("Realtime prompt exceeds 1 MiB.");
      send({ type: "conversation.item.create", item: { type: "message", role: "user", content: [{ type: "input_text", text }] } });
      append({ role: "user", text }); setInput(""); setError(""); beginResponse();
    } catch (cause) { fail(cause instanceof Error ? cause.message : "Could not send Realtime message."); }
  }
  async function recordVoice() {
    if (permissionAttempt.current) { setError("The previous browser microphone request is still pending. Resolve that permission request before recording again."); return; }
    if (!ready.current || pending.current || micPending.current || connectionChanged) return;
    micPending.current = true; const current = generation.current, abort = new AbortController(); micController.current = abort; setRecording("starting"); setError(""); inputAudioBytes.current = 0;
    let attempt: Promise<RealtimeRecorder> | undefined;
    try {
      send({ type: "input_audio_buffer.clear" });
      attempt = startRealtimeRecorder(abort.signal, (bytes) => {
        if (abort.signal.aborted || current !== generation.current) return;
        try { inputAudioBytes.current += bytes.length; if (inputAudioBytes.current > maximumAudioBytes) throw new Error("Recording exceeded 8 MiB. It was discarded."); send({ type: "input_audio_buffer.append", audio: audioBase64(bytes) }); }
        catch (cause) { fail(cause instanceof Error ? cause.message : "Could not stream microphone audio."); }
      });
      permissionAttempt.current = attempt;
      const mic = await attempt;
      if (abort.signal.aborted || current !== generation.current) { await mic.stop(false); return; }
      recorder.current = mic; setRecording("recording");
    } catch (cause) { if (current === generation.current && micController.current === abort) { stopMicrophone(); setRecording("idle"); try { if (ready.current) send({ type: "input_audio_buffer.clear" }); } catch { disconnect(); } setError(cause instanceof Error ? cause.message : "Could not access the microphone."); } }
    finally { if (permissionAttempt.current === attempt) permissionAttempt.current = undefined; }
  }
  async function finishVoice(submitAudio: boolean) {
    if (!micPending.current || micFinishing.current) return;
    micFinishing.current = true;
    const current = generation.current; setRecording("finishing");
    try {
      const mic = recorder.current; if (!mic || !submitAudio) { stopMicrophone(); if (ready.current) send({ type: "input_audio_buffer.clear" }); setRecording("idle"); return; }
      await mic.stop(true);
      if (current !== generation.current || micController.current?.signal.aborted) return;
      if (inputAudioBytes.current < 4800) throw new Error("Record at least 100 ms of audio before sending.");
      send({ type: "input_audio_buffer.commit" });
      append({ role: "user", text: `Voice message · ${(inputAudioBytes.current / 48000).toFixed(1)} s` });
      stopMicrophone(); setRecording("idle"); setError(""); beginResponse();
    } catch (cause) { if (current === generation.current) { stopMicrophone(); setRecording("idle"); try { if (ready.current) send({ type: "input_audio_buffer.clear" }); } catch { disconnect(); } setError(cause instanceof Error ? cause.message : "Could not send recording."); } }
  }
  function cancelResponse() {
    const active = pending.current; if (!active || active.cancelRequested) return;
    active.cancelRequested = true;
    try { if (active.responseID) send({ type: "response.cancel", response_id: active.responseID }); setError("Cancellation requested. Waiting for the provider's final response and usage."); }
    catch (cause) { fail(cause instanceof Error ? cause.message : "Could not cancel Realtime response."); }
  }
  const connected = status === "connected", locked = status !== "disconnected", busy = responding || recording !== "idle";
  const patch = (value: Partial<RealtimeSettings>) => setSettings((previous) => ({ ...previous, ...value }));
  return <div className="playground-workspace playground-config-layout">
    <aside className="playground-side-panel"><section className="playground-parameters-card"><h2>Configurations</h2>{connectionControls}
      <ModelControl label="Realtime model" value={model} models={models} scope={connection} disabled={locked} onUpdate={(value) => { setModel(value); clearHistory(); }} />
      <SelectControl label="Realtime mode" value={settings.mode} disabled={locked} options={[{ value: "text", content: "Text" }, { value: "voice", content: "Voice and text" }]} onUpdate={(mode) => patch({ mode })} />
      <SelectControl label="Realtime API dialect" value={settings.dialect} disabled={locked} options={[{ value: "current", content: "Current Realtime" }, { value: "legacy", content: "Legacy Realtime" }]} onUpdate={(dialect) => patch({ dialect })} />
      {settings.mode === "voice" && <TextControl label="Realtime voice" value={settings.voice} disabled={locked} onUpdate={(voice) => patch({ voice })} />}
      <AreaControl label="Realtime instructions" value={settings.instructions} rows={4} disabled={locked} onUpdate={(instructions) => patch({ instructions })} />
      <TextControl label="Maximum response tokens" value={settings.limit} type="number" disabled={locked} onUpdate={(limit) => patch({ limit })} />
      <p className="muted">Select a model/deployment supporting Realtime and its API dialect. Voice requires audio input/output capabilities. Reconnect to apply configuration changes.</p>
      <p className="muted">Microphone audio uses mono PCM16 at 24 kHz. Turns are sent explicitly; automatic voice activity detection and separately billed input transcription are disabled.</p>
    </section></aside>
    <div className="playground-main-panel"><form className="playground-conversation-card" onSubmit={submit}>
      <div className="playground-output-heading"><h2>Realtime conversation</h2><div className="playground-actions"><GatewayButton view="outlined" disabled={locked} onClick={() => { disconnect(true); }}>Clear Realtime</GatewayButton><GatewayButton view="outlined" onClick={() => { try { setCode({ javascript: realtimeCode("javascript", connection, model, settings), python: realtimeCode("python", connection, model, settings) }); } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid configuration."); } }}>Get Realtime code</GatewayButton></div></div>
      <div className="playground-actions"><span role="status" aria-label="Realtime connection status">{status}</span>{!locked ? <GatewayButton disabled={connectionChanged || !model.trim()} onClick={() => void connect()}>Connect Realtime</GatewayButton> : <GatewayButton view="outlined" onClick={() => disconnect()}>Disconnect Realtime</GatewayButton>}</div>
      {error && <p role="alert" className="error">{error}</p>}
      <section className="playground-transcript" aria-label="Realtime conversation history" aria-live="polite">{turns.map((turn) => <article className={`playground-turn playground-turn-${turn.role}`} key={turn.id}><span className="playground-role-label">{turn.role}{turn.status ? ` · ${turn.status}` : ""}</span>{turn.role === "assistant" ? <MarkdownOutput text={turn.text} /> : <pre>{turn.text}</pre>}{turn.role === "assistant" && <CopyOutput text={turn.text === "Audio response" || turn.text === "No text output" ? "" : turn.text} label={`Copy Realtime response ${turn.id}`} />}{turn.audio && <><audio controls preload="none" src={turn.audio.url} aria-label={`Realtime audio ${turn.id}`} /><a className="text-link" href={turn.audio.url} download={`ai-gateway-realtime-${turn.id}.wav`}>Download Realtime audio</a></>}</article>)}{output && <article className="playground-turn playground-turn-assistant"><span className="playground-role-label">assistant · streaming</span><MarkdownOutput text={output} /></article>}{!turns.length && !output && <p className="muted">Connect before sending a message. Audio responses are played only when you press Play.</p>}</section>
      {!!dropped && <p className="muted">{dropped} older local turns removed to keep history bounded. The provider session still retains its own conversation.</p>}
      <AreaControl label="Realtime message" value={input} rows={3} disabled={!connected || busy || connectionChanged} onUpdate={setInput} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); if (!busy) event.currentTarget.form?.requestSubmit(); } }} />
      <div className="playground-actions"><GatewayButton type="submit" disabled={!connected || busy || connectionChanged || !input.trim()}>Send Realtime message</GatewayButton>{responding && <GatewayButton view="outlined" onClick={cancelResponse}>Cancel Realtime response</GatewayButton>}
        {settings.mode === "voice" && (recording === "idle" ? <GatewayButton view="outlined" disabled={!connected || busy || connectionChanged} onClick={() => void recordVoice()}>Record voice</GatewayButton> : <><span role="status">Microphone: {recording}</span>{recording === "starting" && <span className="muted">Waiting for browser microphone access; Discard cancels this attempt.</span>}<GatewayButton disabled={recording !== "recording" || connectionChanged} onClick={() => void finishVoice(true)}>Stop and send voice</GatewayButton><GatewayButton view="outlined" disabled={recording === "finishing"} onClick={() => void finishVoice(false)}>Discard recording</GatewayButton></>)}
      </div>
    </form><section className="playground-metadata-card"><h2>Realtime metadata</h2><dl className="playground-metadata"><div><dt>Response status</dt><dd>{metadata?.status ?? "—"}</dd></div><div><dt>Latency</dt><dd>{metadata ? `${Math.round(metadata.latencyMS)} ms` : "—"}</dd></div><div><dt>First output</dt><dd>{metadata?.firstTokenMS === undefined ? "—" : `${Math.round(metadata.firstTokenMS)} ms`}</dd></div><div><dt>Input tokens</dt><dd>{metadata?.inputTokens ?? "—"}</dd></div><div><dt>Output tokens</dt><dd>{metadata?.outputTokens ?? "—"}</dd></div><div><dt>Audio input tokens</dt><dd>{metadata?.audioInputTokens ?? "—"}</dd></div><div><dt>Audio output tokens</dt><dd>{metadata?.audioOutputTokens ?? "—"}</dd></div><div><dt>Finalized cost</dt><dd>See Usage &amp; spend</dd></div></dl>{!!events.length && <details><summary>Realtime events ({events.length}/{eventCount})</summary><pre>{events.join("\n")}</pre><p className="muted">Diagnostic summaries omit audio bytes and authentication data.</p></details>}</section></div>
    {code && <ModalFrame label="Realtime connection code" onClose={() => setCode(undefined)}><section className="modal playground-code-dialog"><div className="modal-heading"><h2>Realtime code</h2><button type="button" aria-label="Close Realtime code" className="icon-button" onClick={() => setCode(undefined)}>×</button></div><p>Python uses GATEWAY_API_KEY from the environment; browser JavaScript prompts for a test key. Credentials and issued tickets are excluded. This example sends text; microphone capture remains a browser action.</p><PageTabs label="Realtime code language" value={codeLanguage} items={[{ value: "javascript", label: "JavaScript" }, { value: "python", label: "Python" }]} onUpdate={setCodeLanguage} /><pre aria-label="Realtime code" tabIndex={0}>{code[codeLanguage]}</pre><CopyOutput text={code[codeLanguage]} label="Copy Realtime code" /><GatewayButton view="outlined" onClick={() => setCode(undefined)}>Close</GatewayButton></section></ModalFrame>}
  </div>;
}
