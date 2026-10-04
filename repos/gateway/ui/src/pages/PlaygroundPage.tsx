import { playgroundOrigins } from "./playground/origins";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Checkbox, Select, TextArea, TextInput } from "@gravity-ui/uikit";
import type { SSEEvent } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { PageHeader } from "../components/PageHeader";
import { GatewayButton } from "../components/GatewayButton";
import { PageTabs } from "../components/PageTabs";
import { ToolbarIconButton } from "../components/ToolbarIconButton";
import { GravityThemeScope } from "../components/GravityThemeScope";
import { AreaControl, SelectControl, TextControl } from "./playground/Controls";
import { conversationAttachments, conversationInput, retainConversation } from "./playground/attachments";
import type { Attachment } from "./playground/endpointRequests";
import { PricingControls, defaultPricing, estimateCost } from "./playground/PriceEstimate";
import { RealtimePlayground } from "./playground/RealtimePlayground";
import { EndpointPlayground } from "./playground/EndpointPlayground";
import { endpointPaths, type SpecializedEndpoint } from "./playground/endpointRequests";
import { CompliancePlayground } from "./playground/CompliancePlayground";
import { AgentBuilder } from "./playground/AgentBuilder";
import { ComparePlayground } from "./playground/ComparePlayground";
import { ResourceControls } from "./playground/ResourceControls";
import { checkPolicies, emptyResources, policyChecks, withResources } from "./playground/resources";
import type { CodeCheck } from "./playground/requests";
import { decideTool, editCustomToolResult, executeTool, provideCustomToolResult, toolInvocations, toolOutputs, validateToolContinuation, type ToolInvocation } from "./playground/toolCalls";
import { CopyOutput, OutputDetails } from "./playground/OutputDetails";
import { ToolApprovals } from "./playground/ToolApprovals";
import { CodeDialog } from "./playground/CodeDialog";
import { responsePending, runResponseResource, runText, type TextRun } from "./playground/runText";
import { buildTextRequest, playgroundConnection, textEndpointPaths, type GenerationSettings, type KeySource, type Message } from "./playground/requests";

type PlaygroundMode = "chat" | "responses";
type ModelList = { data?: Array<{ id: string }> };
type TranscriptTurn = { id: number; role: "user" | "assistant" | "tool"; content: string; wire: Message; reasoning?: string; response?: Record<string, unknown> };
type RunMetadata = TextRun;

function sessionID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return `playground-${crypto.randomUUID()}`;
  return `playground-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function PlaygroundPage() {
  const { client: sessionClient } = useAuth();
  const [keySource, setKeySource] = useState<KeySource>("session");
  const [testKey, setTestKey] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [appliedConnection, setAppliedConnection] = useState({ source: "session" as KeySource, key: "", url: "" });
  const connection = useMemo(() => playgroundConnection(sessionClient, appliedConnection.source, appliedConnection.key, appliedConnection.url), [sessionClient, appliedConnection]);
  const client = connection.client;
  const [endpoint, setEndpoint] = useState<"chat" | "responses" | "realtime" | SpecializedEndpoint>("chat");
  const [mode, setMode] = useState<PlaygroundMode>("chat");
  const [view, setView] = useState<"chat" | "compare" | "compliance" | "agents">("chat");
  const [visitedCompare, setVisitedCompare] = useState(false);
  const [visitedCompliance, setVisitedCompliance] = useState(false);
  const [visitedAgents, setVisitedAgents] = useState(false);
  const [models, setModels] = useState<string[]>([]);
  const [modelsError, setModelsError] = useState("");
  const [loadingModels, setLoadingModels] = useState(true);
  const [model, setModel] = useState("");
  const [instructions, setInstructions] = useState("");
  const [message, setMessage] = useState("");
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [readingAttachments, setReadingAttachments] = useState(false);
  const [historyDropped, setHistoryDropped] = useState(0);
  const [pricing, setPricing] = useState(defaultPricing);
  const [maxTokens, setMaxTokens] = useState("256");
  const [temperature, setTemperature] = useState("");
  const [topP, setTopP] = useState("");
  const [responseFormat, setResponseFormat] = useState<GenerationSettings["responseFormat"]>("text");
  const [schema, setSchema] = useState("");
  const [advanced, setAdvanced] = useState("");
  const [resources, setResources] = useState(emptyResources);
  const [calls, setCalls] = useState<ToolInvocation[]>([]);
  const [unreviewableTools, setUnreviewableTools] = useState(false);
  const [toolPrompt, setToolPrompt] = useState("");
  const [apiContinuity, setAPIContinuity] = useState(true);
  const [codeRequest, setCodeRequest] = useState<{ path: string; body: unknown; baseURL: string; checks?: CodeCheck[] }>();
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
  const [pendingResponse, setPendingResponse] = useState<{ result: TextRun; turns: TranscriptTurn[]; prompt?: string; started: number; tools: unknown }>();
  const abortRef = useRef<AbortController | undefined>(undefined);
  const modelAbortRef = useRef<AbortController | undefined>(undefined);
  const modelGeneration = useRef(0);
  const turnID = useRef(0);
  const attachmentGeneration = useRef(0);
  const attachmentInput = useRef<HTMLInputElement>(null);
  const conversationScope = useRef({ connection, model, endpoint });

  async function loadModels() {
    const generation = ++modelGeneration.current;
    modelAbortRef.current?.abort();
    const controller = new AbortController(); modelAbortRef.current = controller;
    setLoadingModels(true); setModelsError("");
    try {
      const payload = await client.request<ModelList>(connection.path("/v1/models"), { signal: controller.signal, maximumResponseBytes: 8 * 1024 * 1024 });
      if (generation !== modelGeneration.current) return;
      const available = [...new Set((payload.data || []).map((item) => item.id.trim()).filter(Boolean))].sort();
      setModels(available);
      setModel((current) => available.includes(current) ? current : available[0] || "");
    } catch (cause) { if (generation === modelGeneration.current && !controller.signal.aborted) { setModels([]); setModel(""); setModelsError(cause instanceof Error ? cause.message : "Could not load models"); } }
    finally { if (generation === modelGeneration.current) setLoadingModels(false); }
  }

  useEffect(() => { void loadModels(); return () => { modelGeneration.current++; modelAbortRef.current?.abort(); }; }, [connection]);
  useEffect(() => () => { attachmentGeneration.current++; abortRef.current?.abort(); abortRef.current = undefined; }, []);

  function newSession() {
    abortRef.current?.abort();
    abortRef.current = undefined; setRunning(false);
    attachmentGeneration.current++; if (attachmentInput.current) attachmentInput.current.value = ""; setAttachments([]); setReadingAttachments(false); setHistoryDropped(0);
    setCalls([]); setUnreviewableTools(false); setToolPrompt(""); setTranscript([]); setPendingOutput(""); setMetadata(undefined); setEvents([]); setEventCount(0); setError("");
    setPreviousResponseID(""); setPendingResponse(undefined); setActiveSessionID(sessionID());
    turnID.current = 0;
  }

  useEffect(() => {
    const previous = conversationScope.current;
    conversationScope.current = { connection, model, endpoint };
    if (previous.connection !== connection || previous.model !== model || previous.endpoint !== endpoint) {
      newSession(); setResources({ ...emptyResources }); setPricing(defaultPricing); setCodeRequest(undefined);
    }
  }, [connection, model, endpoint]);

  function changeMode(next: PlaygroundMode) {
    if (next === mode || running) return;
    setMode(next); setEndpoint(next); newSession();
  }

  function applyConnection() {
    try {
      playgroundConnection(sessionClient, keySource, testKey, baseURL);
      newSession(); setPricing(defaultPricing); setModels([]); setModel("");
      setAppliedConnection({ source: keySource, key: keySource === "custom" ? testKey : "", url: baseURL });
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not configure connection"); }
  }

  function requestBody(input: string | undefined, stream: boolean) {
    if (new TextEncoder().encode(input || "").length > 1024 * 1024) throw new Error("Prompt exceeds the 1 MiB Playground limit.");
    const body = withResources(buildTextRequest({ endpoint: mode, model, input: input === undefined ? undefined : conversationInput(input, attachments), instructions, streaming: stream,
      toolOutputs: input === undefined ? toolOutputs(calls) : [],
      history: transcript.map((turn) => turn.wire),
      previousResponseID: apiContinuity ? previousResponseID : "",
      settings: { maxTokens, temperature, topP, responseFormat, schema, advanced } }), mode, resources);
    if (input === undefined) validateToolContinuation(calls, body.tools);
    return body;
  }

  function getCode() {
    try { setCodeRequest({ path: textEndpointPaths[mode], body: requestBody(calls.length ? undefined : message.trim() || (attachments.length ? "" : "Your message"), streaming), baseURL: connection.baseURL, checks: policyChecks(resources.policies, calls.length ? toolPrompt : message.trim(), model) }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not generate code"); }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (connectionChanged) { setError("Apply connection changes before sending a request."); return; }
    if (pendingResponse) { setError("Refresh or cancel the pending background response before sending a new message."); return; }
    if (unreviewableTools) { setError("Clear the conversation before continuing after an invalid tool response."); return; }
    if (calls.length) { setError("Resolve the pending tool calls and continue before sending a new message."); return; }
    const input = message.trim();
    if (running || readingAttachments || !model.trim() || (!input && !attachments.length)) return;
    await runConversation(input);
  }

  async function runConversation(input?: string) {
    if (running || pendingResponse || unreviewableTools || connectionChanged || abortRef.current) return;
    const started = performance.now();
    const controller = new AbortController();
    abortRef.current = controller;
    setRunning(true); setError(""); setPendingOutput(""); setMetadata(undefined); setEvents([]); setEventCount(0);
    let transportStarted = false;
    try {
      const body = requestBody(input, streaming);
      await checkPolicies(connection, resources.policies, input === undefined ? toolPrompt : input, model, controller.signal);
      transportStarted = true;
      const result = await runText(connection, mode, body, { signal: controller.signal, sessionID: activeSessionID,
        onText: (text) => { if (abortRef.current === controller && !controller.signal.aborted) setPendingOutput(text); } });
      if (abortRef.current !== controller || controller.signal.aborted) return;
      const submittedTurns: TranscriptTurn[] = input === undefined ? toolOutputs(calls).map((wire) => ({ id: ++turnID.current, role: "tool", content: String(wire.content), wire })) : [{ id: ++turnID.current, role: "user", content: input + (attachments.length ? `\nAttachments: ${attachments.map((item) => item.filename).join(", ")}` : ""), wire: { role: "user", content: conversationInput(input, attachments) } }];
      if (mode === "responses" && responsePending(result.response)) {
        setPendingResponse({ result, turns: submittedTurns, prompt: input, started, tools: body.tools });
        setCalls([]); setMetadata(result); setPendingOutput(result.text); setMessage("");
        setAttachments([]); if (attachmentInput.current) attachmentInput.current.value = "";
      } else finishConversation(result, submittedTurns, input, body.tools);
    } catch (cause) {
      if (abortRef.current !== controller) return;
      const aborted = cause && typeof cause === "object" && "name" in cause && cause.name === "AbortError";
      const failure = aborted ? "Request cancelled" : cause instanceof Error ? cause.message : "Request failed";
      setError(failure + (transportStarted && input === undefined && calls.some((call) => call.nativeApproval && call.approved) ? ". Provider continuation may already have executed approved tools; retrying can repeat execution." : ""));
    } finally {
      if (abortRef.current === controller) { abortRef.current = undefined; setRunning(false); }
    }
  }

  function finishConversation(result: TextRun, turns: TranscriptTurn[], input: string | undefined, declaredTools: unknown) {
    let invocations: ToolInvocation[] = [], reviewError = "";
    try { invocations = toolInvocations(mode, result.response, resources.tools, declaredTools); }
    catch (cause) { reviewError = `Tool review failed: ${cause instanceof Error ? cause.message : "Invalid tool calls."} Clear the conversation before continuing.`; }
    setUnreviewableTools(!!reviewError);
    const choice = (result.response.choices as { message?: Message }[] | undefined)?.[0]?.message;
    const assistantTurn: TranscriptTurn = { id: ++turnID.current, role: "assistant", content: result.text, reasoning: result.reasoning, response: result.response,
      wire: { ...choice, role: "assistant", content: mode === "responses" && Array.isArray(result.response.output) ? "" : choice?.content ?? result.text, ...(mode === "responses" && Array.isArray(result.response.output) ? { responseItems: result.response.output } : {}) } };
    const retained = retainConversation([...transcript, ...turns, assistantTurn]);
    if (mode === "responses" && result.id) setPreviousResponseID(result.id);
    setCalls(invocations); if (input !== undefined) setToolPrompt(input);
    setTranscript(retained.turns); setHistoryDropped((value) => value + retained.dropped); setAttachments([]); if (attachmentInput.current) attachmentInput.current.value = "";
    setPendingOutput(""); setMetadata(result); setMessage("");
    if ("events" in result && Array.isArray(result.events)) setEvents(result.events as SSEEvent[]);
    if ("eventCount" in result && typeof result.eventCount === "number") setEventCount(result.eventCount);
    setPendingResponse(undefined);
    if (reviewError) setError(reviewError);
    else if (mode === "responses" && ["failed", "cancelled", "incomplete"].includes(String(result.response.status))) {
      const failure = result.response.error as { message?: unknown } | undefined;
      setError(`Response ${String(result.response.status)}. ${typeof failure?.message === "string" ? failure.message : "Inspect finalized usage before retrying."}`);
    }
  }
  async function manageResponse(operation: "refresh" | "cancel") {
    if (!pendingResponse || running || connectionChanged || abortRef.current) return;
    const job = pendingResponse, controller = new AbortController(); abortRef.current = controller;
    setRunning(true); setError("");
    try {
      const result = await runResponseResource(connection, job.result.id!, operation, controller.signal, activeSessionID);
      if (abortRef.current !== controller || controller.signal.aborted) return;
      result.latencyMS = performance.now() - job.started;
      result.model ||= job.result.model;
      if (responsePending(result.response)) { setPendingResponse({ ...job, result }); setMetadata(result); setPendingOutput(result.text); }
      else finishConversation(result, job.turns, job.prompt, job.tools);
    } catch (cause) {
      if (abortRef.current === controller) setError(controller.signal.aborted ? "Response operation cancelled locally. Server execution may still be active; refresh before retrying." : cause instanceof Error ? cause.message : "Response operation failed.");
    } finally { if (abortRef.current === controller) { abortRef.current = undefined; setRunning(false); } }
  }

  async function approveTool(index: number) {
    if (running || connectionChanged || !calls[index] || calls[index].issue) return;
    if (calls[index].nativeApproval) {
      setCalls((previous) => previous.map((call, position) => position === index ? decideTool(call, true) : call));
      return;
    }
    if (calls[index].customTool || calls[index].output !== undefined) return;
    const controller = new AbortController(); abortRef.current = controller; setRunning(true); setError("");
    const call = calls[index];
    try {
      const output = await executeTool(connection, call, controller.signal);
      if (abortRef.current === controller && !controller.signal.aborted) setCalls((previous) => previous.map((item, position) => position === index ? { ...item, status: "completed", output, error: undefined } : item));
    } catch (cause) {
      if (abortRef.current === controller) setCalls((previous) => previous.map((item, position) => position === index ? { ...item, status: "failed", error: controller.signal.aborted ? "Cancelled. Execution may already have completed; retry uses the same idempotency key." : cause instanceof Error ? cause.message : "Tool execution failed." } : item));
    } finally { if (abortRef.current === controller) { abortRef.current = undefined; setRunning(false); } }
  }

  function customToolResult(index: number, output?: string) {
    if (running || connectionChanged) return;
    setCalls((previous) => previous.map((call, position) => {
      if (position !== index || !call.customTool) return call;
      try { return output === undefined ? provideCustomToolResult(call, call.manualOutput ?? "") : editCustomToolResult(call, output); }
      catch (cause) { return { ...call, output: undefined, status: "pending", error: cause instanceof Error ? cause.message : "Invalid tool result." }; }
    }));
  }

  const usage = pendingResponse ? undefined : metadata?.usage;
  const inputTokens = usage?.prompt_tokens ?? usage?.input_tokens;
  const outputTokens = usage?.completion_tokens ?? usage?.output_tokens;
  const tokenCount = usage?.total_tokens ?? (inputTokens === undefined && outputTokens === undefined ? undefined : (inputTokens ?? 0) + (outputTokens ?? 0));
  const connectionChanged = keySource !== appliedConnection.source || (keySource === "custom" && testKey !== appliedConnection.key) || baseURL !== appliedConnection.url;
  const connectionControls = <>
          <SelectControl label="Virtual key source" value={keySource} disabled={running} options={[{ value: "session", content: "Current UI session" }, { value: "custom", content: "Test API key" }]} onUpdate={(value) => { setKeySource(value); if (value === "session") { setBaseURL(""); setTestKey(""); } }} />
          {keySource === "custom" && <TextControl label="Test API key" type="password" disabled={running} autoComplete="off" value={testKey} onUpdate={setTestKey} placeholder="Enter a gateway virtual key" />}
          <TextControl label="Custom gateway base URL" disabled={running || keySource === "session"} value={baseURL} onUpdate={setBaseURL} placeholder="Optional custom gateway URL" />
          {connectionChanged && <GatewayButton disabled={running} onClick={applyConnection}>Apply connection</GatewayButton>}
          <p className="playground-default-note muted">Active: {connection.source === "session" ? "current UI session" : "test API key"}{connection.baseURL ? ` · ${connection.baseURL}` : " · this gateway"}. Test keys stay in memory.</p>
          <p className="playground-default-note muted">{playgroundOrigins().length ? `Trusted test gateway origins: ${playgroundOrigins().join(", ")}.` : "Custom URLs must use this console’s origin. Ask an administrator to trust an additional test gateway origin."} Realtime requires this console’s origin.</p>
  </>;
  return <>
    <PageHeader eyebrow="Inference" title="Playground" description="Explore models, tune requests and inspect live responses." />
    <PageTabs label="Playground workspace" value={view} items={[{ value: "chat", label: "Chat" }, { value: "compare", label: "Compare" }, { value: "compliance", label: "Compliance" }, { value: "agents", label: "Agent Builder" }]} onUpdate={(next) => { if (running) abortRef.current?.abort(); setCodeRequest(undefined); if (next === "compare") setVisitedCompare(true); if (next === "compliance") setVisitedCompliance(true); if (next === "agents") setVisitedAgents(true); setView(next); }} />
    {view === "chat" && <div className="playground-endpoint-selector"><SelectControl label="Endpoint" value={endpoint} options={[{ value: "chat", content: "/v1/chat/completions" }, { value: "responses", content: "/v1/responses" }, { value: "realtime", content: "/v1/realtime" }, ...Object.entries(endpointPaths).map(([value, path]) => ({ value: value as SpecializedEndpoint, content: path }))]} onUpdate={(next) => { newSession(); setEndpoint(next); if (next === "chat" || next === "responses") setMode(next); }} /></div>}
    {view === "chat" && endpoint !== "chat" && endpoint !== "responses" && endpoint !== "realtime" && <EndpointPlayground key={endpoint} endpoint={endpoint} connection={connection} models={models} connectionControls={connectionControls} connectionChanged={connectionChanged} />}
    {view === "chat" && endpoint === "realtime" && <RealtimePlayground connection={connection} models={models} connectionControls={connectionControls} connectionChanged={connectionChanged} />}
    {view === "chat" && (endpoint === "chat" || endpoint === "responses") && <div className="playground-workspace playground-config-layout">
      <aside className="playground-side-panel" aria-label="Playground configuration">
        <section className="playground-parameters-card">
          <h2>Configurations</h2>
          {connectionControls}
          <PageTabs className="playground-api-tabs" label="Playground API" value={mode} items={[{ value: "chat", label: "Chat Completions" }, { value: "responses", label: "Responses API" }]} onUpdate={changeMode} />
          <label>Model<div className="playground-model-control">
            <GravityThemeScope className="gravity-playground-control">{!loadingModels && !models.length
              ? <TextInput aria-label="Model" size="l" disabled={running} value={model} onUpdate={(value) => { setModel(value); setPricing(defaultPricing); newSession(); }} placeholder="Enter a model ID" />
              : <Select aria-label="Model" size="l" width="max" filterable loading={loadingModels} disabled={running || loadingModels} value={model ? [model] : []} options={models.map((item) => ({ value: item, content: item }))} placeholder="Select an authorized model" onUpdate={([value]) => { setModel(value || ""); setPricing(defaultPricing); newSession(); }} />}
            </GravityThemeScope><ToolbarIconButton icon="refresh" label="Refresh models" disabled={running || loadingModels} onClick={() => void loadModels()} />
          </div><span className="playground-model-help">{models.length ? `${models.length.toLocaleString()} authorized model${models.length === 1 ? "" : "s"}` : "Model access is checked by the gateway"}</span></label>
          {modelsError && <p className="form-error" role="status">Model discovery: {modelsError}</p>}
          <TextControl label="Temperature" disabled={running} type="number" controlProps={{ min: 0, max: 2, step: 0.1 }} value={temperature} onUpdate={setTemperature} placeholder="Provider default" />
          <TextControl label="Maximum output tokens" disabled={running} type="number" controlProps={{ min: 1, step: 1 }} value={maxTokens} onUpdate={setMaxTokens} placeholder="Provider default" />
          <TextControl label="Top P" disabled={running} type="number" controlProps={{ min: 0, max: 1, step: 0.05 }} value={topP} onUpdate={setTopP} placeholder="Provider default" />
          <SelectControl label="Response format" value={responseFormat} disabled={running} options={[{ value: "text", content: "Text" }, { value: "json_object", content: "JSON object" }, { value: "json_schema", content: "JSON schema" }]} onUpdate={setResponseFormat} />
          {responseFormat === "json_schema" && <AreaControl label="Output JSON schema" rows={5} disabled={running} value={schema} onUpdate={setSchema} placeholder='{ "type": "object", "properties": {} }' />}
          <GravityThemeScope className="gravity-playground-control"><Checkbox controlProps={{ "aria-label": "Stream response" }} size="l" disabled={running} checked={streaming} onUpdate={setStreaming}>Stream response</Checkbox></GravityThemeScope>
          {mode === "responses" && <GravityThemeScope className="gravity-playground-control"><Checkbox controlProps={{ "aria-label": "Use API session management" }} disabled={running} checked={apiContinuity} onUpdate={(value) => { setAPIContinuity(value); setPreviousResponseID(""); }}>Use API session management</Checkbox></GravityThemeScope>}
          <details className="playground-advanced"><summary>Advanced parameters</summary><AreaControl label="Advanced parameters JSON" rows={6} disabled={running} value={advanced} onUpdate={setAdvanced} placeholder='{ "reasoning_effort": "low" }' /><p className="muted">Parameters are sent unchanged. Unsupported settings return a gateway or provider error. Responses background mode requires {`{ "background": true }`} and streaming disabled.</p></details>
          <ResourceControls connection={connection} model={model} endpoint={mode} value={resources} onUpdate={setResources} disabled={running || connectionChanged || calls.length > 0 || unreviewableTools || !!pendingResponse} />
          <PricingControls value={pricing} onUpdate={setPricing} disabled={running} />
          <p className="muted playground-default-note">Leave optional settings blank to use provider defaults.</p>
        </section>
      </aside>
      <div className="playground-main-panel">
        <form className="playground-conversation-card" onSubmit={submit}>
          <div className="playground-output-heading"><div><h2>Conversation</h2><span className="muted">Session <code>{activeSessionID}</code></span></div><div className="playground-actions"><GatewayButton view="outlined" disabled={running} onClick={newSession}>Clear</GatewayButton><GatewayButton view="outlined" disabled={running || unreviewableTools || !!pendingResponse || !model.trim()} onClick={getCode}>Get code</GatewayButton></div></div>
          <label htmlFor="playground-instructions">System instructions<GravityThemeScope className="gravity-playground-control"><TextArea id="playground-instructions" controlProps={{ "aria-label": "Instructions" }} size="l" disabled={running} rows={2} value={instructions} onUpdate={setInstructions} placeholder="Optional system instructions" /></GravityThemeScope></label>
          <section className="playground-output" aria-label="Playground conversation">
            {!transcript.length && !pendingOutput && !pendingResponse && <div className="playground-empty"><h3>Start a conversation</h3><p>Choose a model and send a prompt. Conversation content stays in memory.</p><div className="playground-suggestions">{["Explain a complex idea simply", "Draft a short project update", "Review a function for edge cases"].map((prompt) => <button type="button" key={prompt} disabled={running} onClick={() => setMessage(prompt)}>{prompt}</button>)}</div></div>}
            <div className="playground-transcript">{transcript.map((turn) => <article className={`playground-turn ${turn.role}`} key={turn.id}><strong>{turn.role === "user" ? "User" : turn.role === "tool" ? "Tool result" : "Assistant"}</strong><pre>{turn.content || "No text output"}</pre>{turn.reasoning && <details><summary>Reasoning</summary><pre>{turn.reasoning}</pre></details>}{turn.role === "assistant" && <><CopyOutput label={`Copy response ${turn.id}`} text={turn.content} /><OutputDetails payload={turn.response || turn.wire} /></>}{turn.response && <details><summary>Response details</summary><pre>{JSON.stringify(turn.response, null, 2).slice(0, 65536)}</pre></details>}</article>)}{pendingResponse?.turns.map((turn) => <article className={`playground-turn ${turn.role}`} key={turn.id}><strong>{turn.role === "user" ? "User" : "Tool result"}</strong><pre>{turn.content}</pre></article>)}{pendingOutput && <article className={`playground-turn assistant${running ? " streaming" : ""}`}><strong>Assistant <span>{running ? "streaming" : "partial response"}</span></strong><pre>{pendingOutput}</pre></article>}</div>
          </section>
          {pendingResponse && <section aria-label="Background response"><p role="status">Response {pendingResponse.result.id} · {String(pendingResponse.result.response.status)}</p><p className="muted">The server is still executing. Refresh or cancel it before starting another turn. Clear removes only browser state and does not cancel server execution.</p><div className="playground-actions"><GatewayButton disabled={running || connectionChanged} onClick={() => void manageResponse("refresh")}>Refresh background response</GatewayButton><GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => void manageResponse("cancel")}>Cancel background response</GatewayButton></div></section>}
          <ToolApprovals onResultEdit={customToolResult} onUseResult={(index) => customToolResult(index)} calls={calls} disabled={running || connectionChanged} onExecute={(index) => void approveTool(index)} onDecline={(index) => setCalls((previous) => previous.map((item, position) => position === index ? decideTool(item, false) : item))} onContinue={() => void runConversation()} />
          {historyDropped > 0 && <p className="muted">{historyDropped} earlier turns were removed from browser history to keep it bounded. {mode === "responses" && apiContinuity ? "API continuation uses the saved response ID." : "New requests include only the retained browser history."}</p>}
          <label>Images or PDF<input ref={attachmentInput} aria-label="Conversation attachments" type="file" multiple accept="image/png,image/jpeg,image/gif,image/webp,application/pdf" disabled={running || readingAttachments || calls.length > 0 || unreviewableTools || !!pendingResponse} onChange={async (event) => {
            const files = [...(event.currentTarget.files || [])]; const current = ++attachmentGeneration.current; setAttachments([]); setReadingAttachments(true);
            try { const loaded = await conversationAttachments(files); if (current === attachmentGeneration.current) { setAttachments(loaded); setError(""); } }
            catch (cause) { if (current === attachmentGeneration.current) { if (attachmentInput.current) attachmentInput.current.value = ""; setError(cause instanceof Error ? cause.message : "Attachment failed"); } }
            finally { if (current === attachmentGeneration.current) setReadingAttachments(false); }
          }} /><span className="muted">Up to 5 attachments, 8 MiB total. Model and provider compatibility is checked by the gateway.</span></label>
          {attachments.length > 0 && <div className="playground-actions">{attachments.map((item) => <span key={item.filename}>{item.filename}</span>)}<GatewayButton view="flat" disabled={running} onClick={() => { attachmentGeneration.current++; if (attachmentInput.current) attachmentInput.current.value = ""; setAttachments([]); setReadingAttachments(false); }}>Remove conversation attachments</GatewayButton></div>}
          <div className="playground-composer"><GravityThemeScope className="gravity-playground-control"><TextArea id="playground-message" controlProps={{ "aria-label": "Message", required: !attachments.length }} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); if (!running) event.currentTarget.form?.requestSubmit(); } }} size="l" disabled={running || calls.length > 0 || unreviewableTools || !!pendingResponse} rows={4} value={message} onUpdate={setMessage} placeholder="Send a message… Shift+Enter for a new line" /></GravityThemeScope><GatewayButton size="l" type="submit" disabled={running || readingAttachments || connectionChanged || calls.length > 0 || unreviewableTools || !!pendingResponse || !model.trim() || (!message.trim() && !attachments.length)}>{running ? "Running…" : transcript.length ? "Send message" : "Run request"}</GatewayButton>{running && <GatewayButton type="button" view="outlined" size="l" onClick={() => abortRef.current?.abort()}>Stop</GatewayButton>}</div>
          {error && <p role="alert" className="form-error">{error}</p>}
        </form>
        <section className="playground-metadata-card"><h2>Response metadata</h2><dl className="playground-metadata">
          <div><dt>API</dt><dd>{mode === "chat" ? "Chat Completions" : "Responses"}{metadata?.streamed ? " · streamed" : ""}</dd></div>
          <div><dt>Upstream model</dt><dd>{metadata?.model || model || "—"}</dd></div><div><dt>Response ID</dt><dd>{metadata?.id || "—"}</dd></div>
          <div><dt>Status</dt><dd>{String(metadata?.response.status ?? (metadata ? "Response received" : "—"))}</dd></div>
          <div><dt>Input tokens</dt><dd>{inputTokens ?? "—"}</dd></div><div><dt>Output tokens</dt><dd>{outputTokens ?? "—"}</dd></div><div><dt>Tokens</dt><dd>{tokenCount ?? "—"}</dd></div><div><dt>Reasoning tokens</dt><dd>{usage?.completion_tokens_details?.reasoning_tokens ?? usage?.output_tokens_details?.reasoning_tokens ?? "—"}</dd></div>
          <div><dt>{pendingResponse ? "Last observation latency" : metadata?.lifecycle ? "Observed completion latency" : "Total latency"}</dt><dd>{metadata?.latencyMS === undefined ? "—" : `${Math.round(metadata.latencyMS)} ms`}</dd></div><div><dt>Time to first token</dt><dd>{metadata?.firstTokenMS === undefined ? "—" : `${Math.round(metadata.firstTokenMS)} ms`}</dd></div>
          <div><dt>Estimated token cost</dt><dd>{estimateCost(pricing, inputTokens, outputTokens)}</dd></div><div><dt>Finalized cost</dt><dd>See Usage &amp; spend</dd></div>
        </dl>{events.length > 0 && <details className="playground-events"><summary>Stream events ({events.length}{eventCount > events.length ? "+" : ""})</summary><pre>{events.map((item) => `${item.event}: ${item.data}`).join("\n\n")}</pre></details>}</section>
      </div>
    </div>}
    {visitedCompare && <div style={view !== "compare" ? { display: "none" } : undefined}><ComparePlayground active={view === "compare"} connection={connection} models={models} connectionControls={view === "compare" ? connectionControls : null} connectionChanged={connectionChanged} />{view === "compare" && error && <p role="alert" className="form-error">{error}</p>}</div>}
    {visitedCompliance && <div style={view !== "compliance" ? { display: "none" } : undefined}><CompliancePlayground active={view === "compliance"} connection={connection} connectionChanged={connectionChanged} connectionControls={view === "compliance" ? connectionControls : null} /></div>}
    {visitedAgents && <div style={view !== "agents" ? { display: "none" } : undefined}><AgentBuilder active={view === "agents"} connection={connection} models={models} connectionChanged={connectionChanged} connectionControls={view === "agents" ? connectionControls : null} /></div>}
    {codeRequest && <CodeDialog {...codeRequest} onClose={() => setCodeRequest(undefined)} />}
  </>;
}
