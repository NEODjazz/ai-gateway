import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { ModelControl } from "./ModelControl";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { CodeDialog } from "./CodeDialog";
import { buildEndpointRequest, defaultEndpointSettings, readAttachment, safeMediaURL, type Attachment, type EndpointSettings, type SpecializedEndpoint } from "./endpointRequests";
import { runInteractionResource, runNativeText, type NativeTextRun } from "./runNativeText";
import type { PlaygroundConnection } from "./requests";
import { contentText, responsePending } from "./runText";
import { CopyOutput, OutputDetails } from "./OutputDetails";
import { conversationAttachments, retainConversation } from "./attachments";
import { nativeHistory, nativeToolCalls, nativeToolContent, nativeUserContent, validateNativeContinuation, type NativeToolCall, type NativeToolResult, type NativeTurn } from "./nativeConversation";
import { agentApprovalRequest, agentTaskRequest, runAgentRequest, type AgentRun } from "./agents";
import { PricingControls, defaultPricing, estimateCost } from "./PriceEstimate";

import { stringifyExactJSON } from "./exactJSON";
import { AgentToolApprovals } from "./AgentToolApprovals";

type Output = { payload?: Record<string, unknown>; native?: NativeTextRun; agent?: AgentRun; audio?: { url: string; type: string; filename: string }; latencyMS: number };
function object(value: unknown): Record<string, unknown> | undefined { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined; }
export function EndpointPlayground({ endpoint, connection, connectionChanged, connectionControls, models }: {
  endpoint: SpecializedEndpoint; connection: PlaygroundConnection; connectionChanged: boolean; connectionControls: ReactNode; models: string[];
}) {
  const [model, setModel] = useState(models[0] || ""), [input, setInput] = useState(""), [settings, setSettings] = useState<EndpointSettings>(defaultEndpointSettings);
  const [attachments, setAttachments] = useState<Attachment[]>([]), [mask, setMask] = useState<Attachment>();
  const [output, setOutput] = useState<Output>(), [pending, setPending] = useState(""), [error, setError] = useState("");
  const [running, setRunning] = useState(false), [reading, setReading] = useState(false), [code, setCode] = useState<{ path: string; body: unknown; baseURL: string; headers?: Record<string, string>; binaryOutput?: boolean; bodyJSON?: string }>();
  const [history, setHistory] = useState<NativeTurn[]>([]), [previousID, setPreviousID] = useState("");
  const [calls, setCalls] = useState<NativeToolCall[]>([]), [toolResults, setToolResults] = useState<NativeToolResult[]>([]);
  const [unreviewableTools, setUnreviewableTools] = useState(false);
  const [interactionJob, setInteractionJob] = useState<{ id: string; turn: NativeTurn; store: boolean; tools: unknown; started: number; input: string; attachments: Attachment[]; calls: NativeToolCall[]; toolResults: NativeToolResult[] }>();
  const [historyDropped, setHistoryDropped] = useState(0), [pricing, setPricing] = useState(defaultPricing);
  const abort = useRef<AbortController | undefined>(undefined), generation = useRef(0), audioURL = useRef<string | undefined>(undefined);
  const attachmentInput = useRef<HTMLInputElement>(null), maskInput = useRef<HTMLInputElement>(null);
  const textEndpoint = endpoint === "messages" || endpoint === "interactions";
  const conversationEndpoint = textEndpoint || endpoint === "a2a";
  const agentPending = !!output?.agent?.task && output.agent.task.state !== "TASK_STATE_COMPLETED";
  const imageEndpoint = endpoint === "images" || endpoint === "image-edits";
  useEffect(() => () => { generation.current++; abort.current?.abort(); if (audioURL.current) URL.revokeObjectURL(audioURL.current); }, []);
  useEffect(() => { clear(); setInput(""); setAttachments([]); setMask(undefined); if (attachmentInput.current) attachmentInput.current.value = ""; if (maskInput.current) maskInput.current.value = ""; setModel(models[0] || ""); setPricing(defaultPricing); }, [connection]);
  useEffect(() => { if (endpoint !== "a2a" && endpoint !== "mcp" && models.length && !model) { setModel(models[0]); clear(); } }, [models]);
  const patch = (value: Partial<EndpointSettings>) => setSettings((current) => ({ ...current, ...value }));
  function clear() { generation.current++; abort.current?.abort(); abort.current = undefined; setRunning(false); setOutput(undefined); setCode(undefined); setPending(""); setHistory([]); setHistoryDropped(0); setPreviousID(""); setInteractionJob(undefined); setCalls([]); setToolResults([]); setUnreviewableTools(false); setReading(false); setError(""); if (audioURL.current) URL.revokeObjectURL(audioURL.current); audioURL.current = undefined; }
  async function attach(files: FileList | null, isMask = false) {
    if (!files?.length || reading || running) return;
    const current = generation.current; if (isMask) setMask(undefined); else setAttachments([]); setReading(true); setError("");
    try { if (files.length > 8 || [...files].reduce((sum, file) => sum + file.size, 0) > 16 * 1024 * 1024) throw new Error("Choose at most 8 images, up to 16 MiB in total.");
      if (isMask && files[0].type !== "image/png") throw new Error("Image mask must be PNG.");
      const items = conversationEndpoint ? await conversationAttachments([...files]) : await Promise.all([...files].map((file) => readAttachment(file, endpoint === "transcription" ? "audio" : "image")));
      if (current === generation.current) { if (isMask) setMask(items[0]); else setAttachments(items); }
    } catch (cause) { if (current === generation.current) { const field = isMask ? maskInput.current : attachmentInput.current; if (field) field.value = ""; setError(cause instanceof Error ? cause.message : "Attachment failed"); } }
    finally { if (current === generation.current) setReading(false); }
  }
  function body(continuing = false): ReturnType<typeof buildEndpointRequest> & { bodyJSON?: string } {
    if (unreviewableTools) throw new Error("Clear endpoint output before continuing after a tool review failure.");
    if (interactionJob) throw new Error("Refresh or cancel the pending interaction before another turn.");
    if (calls.length && !continuing) throw new Error("Resolve every pending tool call before sending another prompt.");
    if (continuing && (!calls.length || calls.some((call) => !toolResults.find((result) => result.id === call.id)?.text.trim()))) throw new Error("Supply or decline every tool result before continuing.");
    const request = buildEndpointRequest(endpoint, model, continuing ? "" : input, settings, continuing ? [] : attachments, mask, textEndpoint && !previousID ? nativeHistory(endpoint, history) : [], previousID, continuing ? toolResults : [], output?.agent?.task);
    if (textEndpoint && continuing) validateNativeContinuation(endpoint, calls, toolResults, request.body.tools);
    return textEndpoint ? { ...request, bodyJSON: stringifyExactJSON(request.body) } : request;
  }
  function getCode() {
    try { const request = endpoint === "a2a" && agentPending && output?.agent?.task ? agentTaskRequest(settings.agent, output.agent.task, "GetTask") : body(calls.length > 0); setCode({ path: request.path, body: request.body, baseURL: connection.baseURL, headers: request.headers, binaryOutput: endpoint === "speech", bodyJSON: "bodyJSON" in request ? request.bodyJSON : undefined }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not generate code"); }
  }
  async function submit(event?: FormEvent, continuing = false, agentMethod?: "GetTask" | "CancelTask", agentChoices?: { call_id: string; approved: boolean }[]) {
    event?.preventDefault(); if (running || reading || abort.current) return;
    if (connectionChanged) { setError("Apply connection changes before running."); return; }
    let request: ReturnType<typeof body>;
    try { request = agentChoices && output?.agent?.task ? agentApprovalRequest(settings.agent, output.agent.task, agentChoices, settings.agentStream, settings.agentBackground) : agentMethod && output?.agent?.task ? agentTaskRequest(settings.agent, output.agent.task, agentMethod) : body(continuing); } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid request"); return; }
    const controller = new AbortController(); abort.current = controller; const current = ++generation.current, start = performance.now(); setRunning(true); setError(""); setPending(""); if (endpoint !== "a2a") setOutput(undefined);
    try {
      let result: Output;
      if (textEndpoint) {
        const native = await runNativeText(connection, endpoint, request.body, controller.signal, (text) => { if (current === generation.current) setPending(text); });
        result = { native, payload: native.response, latencyMS: native.latencyMS };
        if (current !== generation.current || controller.signal.aborted) return;
        const content = continuing ? nativeToolContent(endpoint, toolResults) : nativeUserContent(endpoint, input, attachments);
        const turn: NativeTurn = { role: continuing ? "tool" : "user", content, text: continuing ? toolResults.map((result) => `${result.id}: ${result.text}`).join("\n") : input || attachments.map((file) => file.filename).join("\n") };
        if (endpoint === "interactions" && responsePending(native.response)) { setInteractionJob({ id: String(native.response.id), turn, store: request.body.store !== false, started: start, tools: request.body.tools, input, attachments, calls, toolResults }); setCalls([]); setToolResults([]); }
        else finishNative(native, turn, request.body.store !== false, request.body.tools);
      } else if (endpoint === "a2a") {
        const publish = (agent: AgentRun) => {
          if (current !== generation.current || controller.signal.aborted) return;
          setOutput({ agent, payload: agent.response, latencyMS: agent.latencyMS });
          const text = agent.text || agent.task?.state || "No text output";
          if (agentMethod || agentChoices) setHistory(history.map((turn, index) => index === history.length - 1 && turn.role === "assistant" ? { ...turn, text } : turn));
          else {
            const retained = retainConversation<NativeTurn>([...history, { role: "user", content: input, text: input || attachments.map((file) => file.filename).join("\n") }, { role: "assistant", content: [], text }]);
            setHistory(retained.turns); setHistoryDropped(historyDropped + retained.dropped);
          }
        };
        const agent = await runAgentRequest(connection, request, controller.signal, publish);
        if (current !== generation.current || controller.signal.aborted) return;
        publish(agent); result = { agent, payload: agent.response, latencyMS: agent.latencyMS };
      } else if (endpoint === "speech") {
        const audio = await connection.client.requestBinary(connection.path(request.path), { maximumResponseBytes: 32 * 1024 * 1024, method: "POST", body: request.body, signal: controller.signal });
        if (current !== generation.current || controller.signal.aborted) return;
        if (audio.body.size > 32 * 1024 * 1024) throw new Error("Audio output exceeds the 32 MiB Playground limit.");
        if (audioURL.current) URL.revokeObjectURL(audioURL.current); const url = URL.createObjectURL(audio.body); audioURL.current = url;
        const type = audio.contentType.split(";")[0].trim().toLowerCase();
        const extension = ({ "audio/mpeg": "mp3", "audio/wav": "wav", "audio/x-wav": "wav", "audio/ogg": "ogg", "audio/flac": "flac", "audio/aac": "aac", "audio/mp4": "m4a", "audio/webm": "webm" } as Record<string, string>)[type] || (/^[a-z0-9]{1,8}$/.test(String(request.body.response_format)) ? String(request.body.response_format) : "bin");
        result = { audio: { url, type, filename: `ai-gateway-speech.${extension}` }, latencyMS: performance.now() - start };
      } else {
        const payload = await connection.client.request<Record<string, unknown>>(connection.path(request.path), { maximumResponseBytes: 32 * 1024 * 1024, method: "POST", body: request.body, headers: request.headers, signal: controller.signal });
        if (!object(payload)) throw new Error("Gateway returned an invalid response.");
        const failure = object(payload.error); if (failure) throw new Error(String(failure.message || "Request failed"));
        if (endpoint === "mcp" && payload.isError === true) throw new Error(contentText(payload.content) || "MCP tool returned an error");
        result = { payload, latencyMS: performance.now() - start };
      }
      if (current !== generation.current || controller.signal.aborted) return;
      setOutput(result); setPending(""); if (conversationEndpoint && !continuing && !agentMethod && !agentChoices && !(endpoint === "interactions" && result.payload?.status === "failed")) { setInput(""); setAttachments([]); if (attachmentInput.current) attachmentInput.current.value = ""; }
    } catch (cause) {
      if (current === generation.current) {
        const cancelled = endpoint === "a2a" ? "Request cancelled. Server execution may already have completed; refresh a known task before repeating it." : "Request cancelled";
        setError(controller.signal.aborted ? cancelled : cause instanceof Error ? cause.message : "Request failed");
      }
    }
    finally { if (current === generation.current) { setRunning(false); abort.current = undefined; } }
  }

  function finishNative(native: NativeTextRun, turn: NativeTurn, store: boolean, tools: unknown) {
    if (endpoint !== "messages" && endpoint !== "interactions") return;
    if (endpoint === "interactions" && native.response.status === "failed") {
      if (interactionJob) { setInput(interactionJob.input); setAttachments(interactionJob.attachments); setCalls(interactionJob.calls); setToolResults(interactionJob.toolResults); }
      setInteractionJob(undefined);
      setError(`Interaction failed. ${String(object(native.response.error)?.message || "Inspect finalized usage before retrying.")}`);
      return;
    }
    const response = endpoint === "interactions" && native.response.steps === undefined && ["failed", "cancelled", "incomplete"].includes(String(native.response.status)) ? { ...native.response, steps: [] } : native.response;
    let nextCalls: NativeToolCall[] = [], reviewError = "";
    try { nextCalls = nativeToolCalls(endpoint, response, tools); }
    catch (cause) { reviewError = `Tool review failed: ${cause instanceof Error ? cause.message : "Invalid native tool calls."} Clear endpoint output before continuing.`; }
    setUnreviewableTools(!!reviewError);
    const retained = retainConversation<NativeTurn>([...history, turn, { role: "assistant", content: endpoint === "messages" ? response.content : response.steps, text: native.text, reasoning: native.reasoning }], stringifyExactJSON);
    setHistory(retained.turns); setHistoryDropped((value) => value + retained.dropped); setCalls(nextCalls); setToolResults([]); setInteractionJob(undefined);
    if (endpoint === "interactions") setPreviousID(store && typeof native.response.id === "string" ? native.response.id : "");
    if (reviewError) setError(reviewError);
    else if (["failed", "cancelled", "incomplete"].includes(String(native.response.status))) setError(`Interaction ${String(native.response.status)}. ${String(object(native.response.error)?.message || "Inspect finalized usage before retrying.")}`);
  }
  async function manageInteraction(operation: "refresh" | "cancel") {
    if (!interactionJob || running || reading || connectionChanged || abort.current) return;
    const job = interactionJob, controller = new AbortController(), current = ++generation.current;
    abort.current = controller; setRunning(true); setError("");
    try {
      const native = await runInteractionResource(connection, job.id, operation, controller.signal);
      if (current !== generation.current || controller.signal.aborted) return;
      native.latencyMS = performance.now() - job.started;
      if (!responsePending(native.response)) finishNative(native, job.turn, job.store, job.tools);
      setOutput({ native, payload: native.response, latencyMS: native.latencyMS }); setPending("");
    } catch (cause) { if (current === generation.current) setError(controller.signal.aborted ? "Interaction operation cancelled locally. Server execution may still be active; refresh before retrying." : cause instanceof Error ? cause.message : "Interaction operation failed."); }
    finally { if (current === generation.current) { abort.current = undefined; setRunning(false); } }
  }
  const payload = output?.payload, native = output?.native;
  const usage = interactionJob ? undefined : object(payload?.usage);
  const inputTokens = usage?.input_tokens ?? usage?.prompt_tokens ?? usage?.total_input_tokens;
  const outputTokens = usage?.output_tokens ?? usage?.completion_tokens ?? usage?.total_output_tokens;
  const images = imageEndpoint && Array.isArray(payload?.data) ? payload.data.slice(0, 10).map((value) => { const item = object(value); return { src: safeMediaURL(item?.b64_json ? `data:image/png;base64,${item.b64_json}` : item?.url), prompt: item?.revised_prompt }; }) : [];
  const embeddings = endpoint === "embeddings" && Array.isArray(payload?.data) ? payload.data.slice(0, 100).map((value) => object(value)) : [];

  return <div className="playground-workspace playground-config-layout">
    <aside className="playground-side-panel"><section className="playground-parameters-card"><h2>Configurations</h2>{connectionControls}
      {!["a2a", "mcp"].includes(endpoint) && <ModelControl label="Endpoint model" value={model} models={models} scope={connection} disabled={running} onUpdate={(value) => { setModel(value); setPricing(defaultPricing); clear(); }} />}
      {textEndpoint && <><AreaControl label="Native system instructions" rows={3} value={settings.instructions} disabled={running} onUpdate={(instructions) => patch({ instructions })} /><TextControl label="Native maximum output tokens" value={settings.limit} type="number" disabled={running} onUpdate={(limit) => patch({ limit })} /><TextControl label="Native temperature" value={settings.temperature} type="number" disabled={running} onUpdate={(temperature) => patch({ temperature })} /><TextControl label="Native Top P" value={settings.topP} type="number" disabled={running} onUpdate={(topP) => patch({ topP })} /><GravityThemeScope><Checkbox controlProps={{ "aria-label": "Stream native response" }} checked={settings.stream} disabled={running} onUpdate={(stream) => patch({ stream })}>Stream response</Checkbox></GravityThemeScope></>}
      {imageEndpoint && <><TextControl label="Image size" value={settings.size} disabled={running} onUpdate={(size) => patch({ size })} placeholder="Provider default" /><TextControl label="Image count" type="number" value={settings.count} disabled={running} onUpdate={(count) => patch({ count })} /><TextControl label="Image quality" value={settings.quality} disabled={running} onUpdate={(quality) => patch({ quality })} placeholder="Provider default" /></>}
      {endpoint === "embeddings" && <TextControl label="Embedding dimensions" value={settings.dimensions} disabled={running} type="number" onUpdate={(dimensions) => patch({ dimensions })} placeholder="Model default" />}
      {endpoint === "speech" && <><TextControl label="Speech voice" value={settings.voice} disabled={running} onUpdate={(voice) => patch({ voice })} /><SelectControl label="Speech format" value={settings.format} disabled={running} options={["mp3", "wav", "opus", "aac", "flac", "pcm"].map((value) => ({ value, content: value }))} onUpdate={(format) => patch({ format })} /><TextControl label="Speech speed" type="number" value={settings.speed} disabled={running} onUpdate={(speed) => patch({ speed })} /></>}
      {endpoint === "transcription" && <TextControl label="Transcription language" value={settings.language} disabled={running} onUpdate={(language) => patch({ language })} placeholder="Optional language code" />}
      {endpoint === "a2a" && <TextControl label="Agent ID" value={settings.agent} disabled={running} onUpdate={(agent) => { patch({ agent }); clear(); setInput(""); setAttachments([]); if (attachmentInput.current) attachmentInput.current.value = ""; }} />}
      {endpoint === "a2a" && <><GravityThemeScope><Checkbox controlProps={{ "aria-label": "Stream endpoint agent task" }} checked={settings.agentStream} disabled={running || settings.agentBackground} onUpdate={(agentStream) => patch({ agentStream })}>Stream task updates</Checkbox></GravityThemeScope><GravityThemeScope><Checkbox controlProps={{ "aria-label": "Run endpoint agent in background" }} checked={settings.agentBackground} disabled={running || settings.agentStream} onUpdate={(agentBackground) => patch({ agentBackground })}>Run in background</Checkbox></GravityThemeScope>{settings.agentBackground && <p className="muted">Queues a server task. Use Refresh to observe completion or tool review, and Cancel to request cancellation. Closing a local request does not cancel server execution.</p>}{settings.agentStream && <p className="muted">Task streaming reports status and text artifacts. Token usage and first-token timing are unavailable.</p>}</>}
      {endpoint === "mcp" && <><TextControl label="MCP server ID" value={settings.server} disabled={running} onUpdate={(server) => patch({ server })} /><TextControl label="MCP tool name" value={settings.tool} disabled={running} onUpdate={(tool) => patch({ tool })} /><AreaControl label="MCP arguments JSON" rows={6} value={settings.arguments} disabled={running} onUpdate={(args) => patch({ arguments: args })} /></>}
      {textEndpoint && <PricingControls disabled={running} value={pricing} onUpdate={setPricing} />}
      {endpoint !== "a2a" && <details><summary>Advanced endpoint parameters</summary><AreaControl label="Endpoint parameters JSON" rows={5} value={settings.advanced} disabled={running} onUpdate={(advanced) => patch({ advanced })} /><p className="muted">Uses this endpoint's native parameter dialect. Unsupported parameters produce an error.{endpoint === "interactions" && <> Background interactions require {`{ "background": true }`} and streaming disabled.</>}</p></details>}
    </section></aside>
    <div className="playground-main-panel"><form className="playground-conversation-card" onSubmit={submit}>
      <div className="playground-output-heading"><h2>{conversationEndpoint ? endpoint === "a2a" ? "Agent conversation" : "Native conversation" : "Endpoint request"}</h2><div className="playground-actions"><GatewayButton view="outlined" disabled={running} onClick={clear}>Clear endpoint output</GatewayButton><GatewayButton view="outlined" disabled={running || !!interactionJob || unreviewableTools} onClick={getCode}>Get endpoint code</GatewayButton></div></div>
      {endpoint !== "mcp" && <AreaControl label={endpoint === "speech" ? "Speech text" : endpoint === "transcription" ? "Transcription prompt" : endpoint === "embeddings" ? "Embedding input" : "Endpoint input"} rows={5} value={input} disabled={running || calls.length > 0 || agentPending || !!interactionJob || unreviewableTools} onUpdate={setInput} onKeyDown={(event) => { if (conversationEndpoint && event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); if (!running) event.currentTarget.form?.requestSubmit(); } }} />}
      {(endpoint === "image-edits" || endpoint === "transcription" || conversationEndpoint) && <><label>Input {endpoint === "transcription" ? "audio" : conversationEndpoint ? "images or PDF" : "images"}<input ref={attachmentInput} aria-label="Endpoint attachment" type="file" multiple={endpoint === "image-edits" || conversationEndpoint} disabled={running || reading || calls.length > 0 || agentPending || !!interactionJob || unreviewableTools} accept={endpoint === "transcription" ? "audio/*,.webm" : `image/png,image/jpeg,image/webp,image/gif${conversationEndpoint ? ",application/pdf" : ""}`} onChange={(event) => void attach(event.currentTarget.files)} /></label>{attachments.map((item) => <p key={item.filename}>{item.filename} · {item.media_type}</p>)}<GatewayButton view="flat" disabled={running || reading} onClick={() => { setAttachments([]); if (attachmentInput.current) attachmentInput.current.value = ""; }}>Remove attachments</GatewayButton></>}
      {endpoint === "image-edits" && <label>Optional mask<input ref={maskInput} aria-label="Image mask attachment" type="file" accept="image/png" disabled={running || reading} onChange={(event) => void attach(event.currentTarget.files, true)} />{mask && <><span>{mask.filename}</span><GatewayButton view="flat" disabled={running} onClick={() => { setMask(undefined); if (maskInput.current) maskInput.current.value = ""; }}>Remove mask</GatewayButton></>}</label>}
      {endpoint === "mcp" && <p className="muted">Running this request executes the selected tool with these arguments and a unique idempotency key. Review the tool and arguments before execution.</p>}
      <div className="playground-actions"><GatewayButton type="submit" disabled={running || reading || connectionChanged || calls.length > 0 || agentPending || !!interactionJob || unreviewableTools}>{running ? "Running…" : endpoint === "mcp" ? "Execute MCP tool" : "Run endpoint request"}</GatewayButton>{running && <GatewayButton view="outlined" onClick={() => abort.current?.abort()}>Stop endpoint request</GatewayButton>}</div>
      {error && <p role="alert" className="form-error">{error}</p>}{pending && <pre className="playground-native-text">{pending}</pre>}
      {conversationEndpoint && <section className="playground-transcript" aria-label={endpoint === "a2a" ? "Agent conversation history" : "Native conversation history"}>{[...history, ...(interactionJob ? [interactionJob.turn] : [])].map((turn, index) => <article className={`playground-turn ${turn.role}`} key={index}><strong>{turn.role === "user" ? "User" : turn.role === "tool" ? "Tool result" : endpoint === "a2a" ? "Agent" : "Assistant"}</strong><pre>{(turn.text ?? contentText(turn.content)) || "No text output"}</pre>{turn.role === "assistant" && <><CopyOutput label={`Copy ${endpoint === "a2a" ? "agent" : "native"} response ${index + 1}`} text={turn.text ?? contentText(turn.content)} />{turn.reasoning && <details><summary>Reasoning</summary><pre>{turn.reasoning}</pre></details>}<OutputDetails payload={{ content: turn.content }} encodeJSON={textEndpoint ? stringifyExactJSON : undefined} /></>}</article>)}</section>}
      {endpoint === "interactions" && payload?.status === "failed" && <section aria-label="Failed interaction output" className="playground-turn assistant"><strong>Assistant · failed</strong><pre>{native?.text || "No text output"}</pre>{native?.reasoning && <details><summary>Reasoning</summary><pre>{native.reasoning}</pre></details>}<CopyOutput text={native?.text || ""} label="Copy failed interaction" /><OutputDetails payload={payload} encodeJSON={stringifyExactJSON} /><p className="muted">This failed interaction is excluded from conversation history. Review its reported usage before explicitly retrying.</p></section>}
      {interactionJob && <section aria-label="Background interaction"><p role="status">Interaction {interactionJob.id} · {String(payload?.status)}</p><p className="muted">Refresh or cancel this interaction before another turn. Clearing removes local state without cancelling server execution.</p><div className="playground-actions"><GatewayButton disabled={running || connectionChanged} onClick={() => void manageInteraction("refresh")}>Refresh background interaction</GatewayButton><GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => void manageInteraction("cancel")}>Cancel background interaction</GatewayButton></div></section>}
      {calls.length > 0 && <section className="playground-tool-approvals" aria-label="Native tool results"><h3>Pending tool calls</h3><p className="muted">Review the arguments, supply a result from your tool, or decline the call. Tools do not execute automatically.</p>{calls.map((call) => { const result = toolResults.find((item) => item.id === call.id); return <article className="playground-turn tool" key={call.id}><strong>{call.name} · {call.id}</strong><pre>{call.rawArguments}</pre>{call.issue && <p role="status" className="form-error">{call.issue}</p>}<AreaControl label={`Tool result ${call.id}`} rows={3} value={result?.text || ""} disabled={running || connectionChanged || !!call.issue} onUpdate={(text) => setToolResults((items) => [...items.filter((item) => item.id !== call.id), { id: call.id, text, declined: false }])} /><GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => setToolResults((items) => [...items.filter((item) => item.id !== call.id), { id: call.id, text: "Tool execution declined by the user.", declined: true }])}>Decline {call.name}</GatewayButton>{result?.declined && <p>Declined</p>}</article>; })}<GatewayButton disabled={running || connectionChanged} onClick={() => void submit(undefined, true)}>Continue native tool results</GatewayButton></section>}
      {historyDropped > 0 && <p className="muted">{historyDropped} earlier turns removed to keep browser history bounded.</p>}
      {images.map((item, index) => <figure key={index}>{item.src ? <><img className="playground-generated-image" src={item.src} alt={`Generated image ${index + 1}`} referrerPolicy="no-referrer" /><a href={item.src} download={`ai-gateway-image-${index + 1}.png`} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">Open/download image</a></> : <p>Image output URL is unavailable or unsupported.</p>}{typeof item.prompt === "string" && <figcaption>{item.prompt}</figcaption>}</figure>)}
      {output?.audio && <div>{/^audio\/(mpeg|wav|x-wav|ogg|flac|mp4|webm|aac)$/.test(output.audio.type) ? <audio controls src={output.audio.url} aria-label="Generated speech" /> : <p>Audio format has no browser preview. Download to play it.</p>}<a href={output.audio.url} download={output.audio.filename}>Download speech</a></div>}
      {!!embeddings.length && <section aria-label="Embedding vectors"><h3>Embedding vectors</h3>{embeddings.map((item, index) => <details key={index}><summary>Vector {String(item?.index ?? index)} · {Array.isArray(item?.embedding) ? item.embedding.length : "—"} dimensions</summary><pre>{JSON.stringify(item?.embedding ?? null).slice(0, 65536)}</pre></details>)}</section>}
      {endpoint === "transcription" && typeof payload?.text === "string" && <section><h3>Transcript</h3><pre>{payload.text}</pre><CopyOutput text={payload.text} /></section>}
      {output?.agent && <section aria-label="Agent task state"><h3>Agent response</h3><p role="status">{output.agent.task ? `Task ${output.agent.task.id} · ${output.agent.task.state}` : "Stateless agent response: each request is independent."}</p>{output.agent.task && <div className="playground-actions"><GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => void submit(undefined, false, "GetTask")}>Refresh endpoint task</GatewayButton>{["TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED"].includes(output.agent.task.state) && <GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => void submit(undefined, false, "CancelTask")}>Cancel endpoint task</GatewayButton>}</div>}{output.agent.task && <AgentToolApprovals label="Endpoint agent tool approvals" task={output.agent.task} disabled={running || connectionChanged} onContinue={(choices) => void submit(undefined, false, undefined, choices)} />}{agentPending && <p className="muted">Resolve the current task or clear endpoint output before starting another conversation.</p>}</section>}
      {endpoint === "mcp" && payload && <section><h3>Tool output</h3><pre>{contentText(payload.content) || (textEndpoint ? stringifyExactJSON(payload) : JSON.stringify(payload, null, 2)).slice(0, 65536)}</pre></section>}
      {payload && !textEndpoint && <OutputDetails payload={payload} connection={connection} disabled={connectionChanged} encodeJSON={endpoint === "a2a" ? stringifyExactJSON : undefined} />}
      {payload && <details><summary>Response details</summary><pre>{((textEndpoint || endpoint === "a2a") ? stringifyExactJSON(payload) : JSON.stringify(payload, null, 2)).slice(0, 65536)}</pre></details>}
    </form><section className="playground-metadata-card"><h2>Response metadata</h2><dl className="playground-metadata"><div><dt>Status</dt><dd>{String(output?.agent?.task?.state ?? payload?.status ?? payload?.stop_reason ?? (output ? "Response received" : "—"))}</dd></div><div><dt>{interactionJob ? "Last observation latency" : native?.lifecycle ? "Observed completion latency" : "Latency"}</dt><dd>{output ? `${Math.round(output.latencyMS)} ms` : "—"}</dd></div><div><dt>First token</dt><dd>{native?.firstTokenMS === undefined ? "—" : `${Math.round(native.firstTokenMS)} ms`}</dd></div><div><dt>Input tokens</dt><dd>{String(usage?.input_tokens ?? usage?.prompt_tokens ?? usage?.total_input_tokens ?? "—")}</dd></div><div><dt>Output tokens</dt><dd>{String(usage?.output_tokens ?? usage?.completion_tokens ?? usage?.total_output_tokens ?? "—")}</dd></div>{textEndpoint && <><div><dt>Estimated token cost</dt><dd>{estimateCost(pricing, typeof inputTokens === "number" ? inputTokens : undefined, typeof outputTokens === "number" ? outputTokens : undefined)}</dd></div><div><dt>Finalized cost</dt><dd>See Usage &amp; spend</dd></div></>}</dl>{native?.events.length ? <details><summary>Native events ({native.events.length}/{native.eventCount})</summary><pre>{native.events.map((event) => `${event.event}: ${event.data}`).join("\n\n")}</pre></details> : null}</section></div>
    {code && <CodeDialog {...code} onClose={() => setCode(undefined)} />}
  </div>;
}
