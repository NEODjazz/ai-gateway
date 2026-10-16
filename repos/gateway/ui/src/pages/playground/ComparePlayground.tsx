import { MarkdownOutput } from "./MarkdownOutput";
import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GatewayButton } from "../../components/GatewayButton";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { ModelControl } from "./ModelControl";
import { AreaControl, SelectControl, TextControl } from "./Controls";
import { buildTextRequest, defaultGenerationSettings, type GenerationSettings, type Message, type PlaygroundConnection } from "./requests";
import { runText, type TextRun } from "./runText";
import { PricingControls, defaultPricing, estimateCost, type PricingInputs } from "./PriceEstimate";
import { conversationAttachments, conversationInput, retainConversation } from "./attachments";
import type { Attachment } from "./endpointRequests";
import { contentText } from "./runText";
import { CopyOutput, OutputDetails } from "./OutputDetails";
import { ResourceControls } from "./ResourceControls";
import { checkPolicies, emptyResources, parseResourceCatalog, policyChecks, withResources, type ResourceCatalog, type ResourceSelection } from "./resources";
import { ToolApprovals } from "./ToolApprovals";
import { decideTool, editManualToolResult, executeTool, provideManualToolResult, toolInvocations, toolOutputs, validateToolContinuation, type ToolInvocation } from "./toolCalls";
import { agentApprovalRequest, agentRequest, agentTaskRequest, runAgentRequest, type AgentRun, type AgentTask } from "./agents";
import { csvCell } from "../../csv";

import { AgentToolApprovals } from "./AgentToolApprovals";

type Panel = {
  id: number; model: string; settings: GenerationSettings; instructions: string; sessionID: string;
  kind: "model" | "agent"; agent: string; agentRun?: AgentRun; calls: ToolInvocation[]; policyPrompt: string;
  resources: ResourceSelection; historyDropped: number; pricing: PricingInputs; history: Message[]; pending: string; lastPrompt?: string; result?: TextRun; error: string; status: "idle" | "running" | "complete" | "awaiting_tools" | "awaiting_task" | "failed" | "cancelled";
  unreviewableTools: boolean;
};

function newPanel(id: number, model: string): Panel {
  return { id, model, settings: { ...defaultGenerationSettings }, instructions: "", sessionID: `playground-compare-${crypto.randomUUID()}`,
    kind: "model", agent: "", calls: [], policyPrompt: "",
    resources: { ...emptyResources }, historyDropped: 0, pricing: { ...defaultPricing }, history: [], pending: "", error: "", status: "idle", unreviewableTools: false };
}

export function ComparePlayground({ connection, models, connectionControls, connectionChanged, active = true }: {
  connection: PlaygroundConnection; models: string[]; connectionControls: ReactNode; connectionChanged: boolean; active?: boolean;
}) {
  const [panels, setPanels] = useState(() => [newPanel(1, models[0] || ""), newPanel(2, models[1] || models[0] || "")]);
  const [prompt, setPrompt] = useState("");
  const [attachments, setAttachments] = useState<Attachment[]>([]), [reading, setReading] = useState(false);
  const attachmentInput = useRef<HTMLInputElement>(null), attachmentGeneration = useRef(0);
  function clearAttachments() { attachmentGeneration.current++; setAttachments([]); setReading(false); if (attachmentInput.current) attachmentInput.current.value = ""; }
  const [sync, setSync] = useState(true);
  const [stream, setStream] = useState(true), [agentBackground, setAgentBackground] = useState(false);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [agents, setAgents] = useState<ResourceCatalog["agents"]>([]), [agentsError, setAgentsError] = useState(""), [loadingAgents, setLoadingAgents] = useState(false);
  const agentAbort = useRef<AbortController | undefined>(undefined), agentEpoch = useRef(0);
  const nextID = useRef(3);
  const generation = useRef(0);
  const controllers = useRef(new Map<number, AbortController>());
  const previousConnection = useRef(connection);
  useEffect(() => { if (!active || connectionChanged) cancel(); }, [active, connectionChanged]);
  useEffect(() => () => { generation.current++; attachmentGeneration.current++; agentEpoch.current++; agentAbort.current?.abort(); for (const controller of controllers.current.values()) controller.abort(); controllers.current.clear(); }, []);
  useEffect(() => {
    if (previousConnection.current === connection) return;
    previousConnection.current = connection; generation.current++; clearAttachments();
    agentEpoch.current++; agentAbort.current?.abort(); setAgents([]); setAgentsError(""); setLoadingAgents(false);
    for (const controller of controllers.current.values()) controller.abort(); controllers.current.clear();
    setRunning(false); setError("");
    setPanels((current) => current.map((panel, index) => ({ ...newPanel(panel.id, models[index] || models[0] || ""), settings: panel.settings, instructions: panel.instructions })));
  }, [connection]);
  useEffect(() => {
    if (!models.length) return;
    if (panels.some((panel) => panel.kind === "model" && !panel.model)) {
      cancel();
      setPanels((current) => current.map((panel, index) => panel.kind === "agent" || panel.model ? panel : { ...newPanel(panel.id, models[index] || models[0]), settings: panel.settings, instructions: panel.instructions }));
    }
  }, [models]);

  async function loadAgents() {
    if (running || connectionChanged || loadingAgents) return;
    const controller = new AbortController(), epoch = ++agentEpoch.current; agentAbort.current?.abort(); agentAbort.current = controller; setLoadingAgents(true); setAgentsError("");
    try {
      const catalog = parseResourceCatalog(await connection.client.request<unknown>(connection.path("/v1/playground/catalog"), { signal: controller.signal, maximumResponseBytes: 1024 * 1024 }));
      if (agentEpoch.current !== epoch || controller.signal.aborted) return;
      setAgents(catalog.agents.filter((item) => item.execution_supported));
      setPanels((items) => items.map((item) => {
        const found = item.kind === "agent" && catalog.agents.find((entry) => entry.id === item.agent);
        return found && found.model !== item.model ? { ...newPanel(item.id, found.model), kind: "agent", agent: item.agent } : item;
      }));
      if (catalog.truncated) setAgentsError("Resource catalog is truncated. Enter an authorized agent ID if it is not shown.");
    } catch (cause) { if (agentEpoch.current === epoch && !controller.signal.aborted) { setAgents([]); setAgentsError(cause instanceof Error ? cause.message : "Could not load authorized agents."); } }
    finally { if (agentEpoch.current === epoch) { setLoadingAgents(false); agentAbort.current = undefined; } }
  }

  function settings(id: number, patch: Partial<GenerationSettings>) {
    setPanels((current) => current.map((panel) => sync || panel.id === id ? { ...panel, settings: { ...panel.settings, ...patch } } : panel));
  }
  function updateInstructions(id: number, value: string) {
    setPanels((current) => current.map((panel) => sync || panel.id === id ? { ...panel, instructions: value } : panel));
  }
  function cancel() {
    generation.current++;
    const activeIDs = new Set(controllers.current.keys());
    for (const controller of controllers.current.values()) controller.abort(); controllers.current.clear();
    setPanels((current) => current.map((panel) => panel.status === "running" ? { ...panel, status: "cancelled", error: "Request cancelled" } : activeIDs.has(panel.id) ? { ...panel, error: "Request cancelled. Server execution may have completed.", calls: panel.calls.map((call) => call.output === undefined ? { ...call, status: "failed", error: "Tool execution cancelled." } : call) } : panel));
    setRunning(false);
  }
  const pendingApprovals = panels.some((panel) => panel.unreviewableTools || panel.calls.length > 0 || panel.agentRun?.task && panel.agentRun.task.state !== "TASK_STATE_COMPLETED");
  type Prepared = { panel: Panel; body?: Record<string, unknown>; agentRequest?: ReturnType<typeof agentRequest>; turns: Message[]; policyPrompt: string; lastPrompt?: string };
  async function runPrepared(requests: Prepared[]) {
    const activeGeneration = ++generation.current;
    setError(""); setRunning(true);
    const ids = new Set(requests.map(({ panel }) => panel.id));
    setPanels((current) => current.map((panel) => ids.has(panel.id) ? { ...panel, status: "running", error: "", pending: "", result: undefined } : panel));
    await Promise.all(requests.map(async ({ panel, body, agentRequest: request, turns, policyPrompt, lastPrompt }) => {
      const controller = new AbortController(); controllers.current.set(panel.id, controller);
      try {
        if (request) {
          const publish = (result: AgentRun, final = false) => {
            if (generation.current !== activeGeneration || controller.signal.aborted) return;
            const retained = retainConversation([...panel.history, ...turns, { role: "assistant", content: result.text || result.task?.state || "No text output" }]);
            const state = result.task?.state, failed = !!state && ["TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED"].includes(state);
            setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, historyDropped: panel.historyDropped + retained.dropped, agentRun: result, pending: "", history: retained.turns, lastPrompt: lastPrompt ?? item.lastPrompt, status: !final ? "running" : failed ? "failed" : !state || state === "TASK_STATE_COMPLETED" ? "complete" : "awaiting_task", error: final && failed ? `Agent task ended in ${state}.` : "" } : item));
          };
          publish(await runAgentRequest(connection, request, controller.signal, (result) => publish(result)), true);
        } else {
          await checkPolicies(connection, panel.resources.policies, policyPrompt, panel.model, controller.signal);
          const result = await runText(connection, "chat", body!, { signal: controller.signal, sessionID: panel.sessionID,
            onText: (text) => { if (generation.current === activeGeneration && !controller.signal.aborted) setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, pending: text } : item)); } });
          if (generation.current !== activeGeneration || controller.signal.aborted) return;
          const choice = (result.response.choices as { message?: Message }[] | undefined)?.[0]?.message;
          const assistant: Message = { ...choice, role: "assistant", content: choice?.content ?? result.text };
          let calls: ToolInvocation[] = [], reviewError = "";
          try { calls = toolInvocations("chat", result.response, panel.resources.tools, body!.tools); }
          catch (cause) { reviewError = `Tool review failed: ${cause instanceof Error ? cause.message : "Invalid tool calls."} Clear this comparison before continuing.`; }
          const retained = retainConversation([...panel.history, ...turns, assistant]);
          setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, calls, unreviewableTools: !!reviewError, error: reviewError, policyPrompt, historyDropped: item.historyDropped + retained.dropped, result, pending: "", status: reviewError ? "failed" : calls.length ? "awaiting_tools" : "complete", history: retained.turns, lastPrompt: lastPrompt ?? item.lastPrompt } : item));
        }
      } catch (cause) {
        if (generation.current === activeGeneration) setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, status: controller.signal.aborted ? "cancelled" : "failed", error: cause instanceof Error ? cause.message : "Request failed" } : item));
      } finally { if (controllers.current.get(panel.id) === controller) controllers.current.delete(panel.id); }
    }));
    if (generation.current === activeGeneration) { setRunning(false); return true; }
    return false;
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    const input = prompt.trim();
    if (running || reading || (!input && !attachments.length) || panels.some((panel) => panel.kind === "model" ? !panel.model : !panel.agent)) return;
    if (connectionChanged) { setError("Apply connection changes before comparing."); return; }
    if (pendingApprovals) { setError("Resolve pending tools and agent tasks before sending a new shared prompt."); return; }
    const wireInput = conversationInput(input, attachments);
    const displayInput = input + (attachments.length ? `\nAttachments: ${attachments.map((item) => item.filename).join(", ")}` : "");
    let requests: Prepared[];
    try {
      if (new TextEncoder().encode(input).length > 1024 * 1024) throw new Error("Prompt exceeds the 1 MiB Playground limit.");
      requests = panels.map((panel) => {
        if (panel.kind === "agent") return { panel, agentRequest: agentRequest(panel.agent, input, panel.agentRun?.task, attachments, stream && !agentBackground, agentBackground), turns: [{ role: "user", content: wireInput }], policyPrompt: input, lastPrompt: displayInput };
        policyChecks(panel.resources.policies, input, panel.model);
        return { panel, body: withResources(buildTextRequest({ endpoint: "chat", model: panel.model, input: wireInput, instructions: panel.instructions, history: panel.history, streaming: stream, settings: panel.settings }), "chat", panel.resources), turns: [{ role: "user", content: wireInput }], policyPrompt: input, lastPrompt: displayInput };
      });
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid comparison settings"); return; }
    if (await runPrepared(requests)) { setPrompt(""); clearAttachments(); }
  }
  async function continuePanel(panel: Panel) {
    if (running || connectionChanged || panel.unreviewableTools) return;
    try {
      const outputs = toolOutputs(panel.calls);
      const body = withResources(buildTextRequest({ endpoint: "chat", model: panel.model, instructions: panel.instructions, history: panel.history, toolOutputs: outputs, streaming: stream, settings: panel.settings }), "chat", panel.resources);
      validateToolContinuation(panel.calls, body.tools);
      policyChecks(panel.resources.policies, panel.policyPrompt, panel.model);
      await runPrepared([{ panel, body, turns: outputs, policyPrompt: panel.policyPrompt }]);
    } catch (cause) { setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, error: cause instanceof Error ? cause.message : "Could not continue comparison." } : item)); }
  }
  async function approveTool(panel: Panel, index: number) {
    if (running || connectionChanged || !panel.calls[index] || panel.calls[index].manualFunction || panel.calls[index].issue || panel.calls[index].output !== undefined) return;
    const controller = new AbortController(), activeGeneration = ++generation.current; controllers.current.set(panel.id, controller); setRunning(true);
    try {
      const output = await executeTool(connection, panel.calls[index], controller.signal);
      if (generation.current === activeGeneration && !controller.signal.aborted) setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, calls: item.calls.map((call, position) => position === index ? { ...call, status: "completed", output, error: undefined } : call) } : item));
    } catch (cause) { if (generation.current === activeGeneration) setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, calls: item.calls.map((call, position) => position === index ? { ...call, status: "failed", error: controller.signal.aborted ? "Tool execution cancelled." : cause instanceof Error ? cause.message : "Tool execution failed." } : call) } : item)); }
    finally { if (controllers.current.get(panel.id) === controller) controllers.current.delete(panel.id); if (generation.current === activeGeneration) setRunning(false); }
  }
  function manualToolResult(panel: Panel, index: number, output?: string) {
    if (running || connectionChanged) return;
    setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, calls: item.calls.map((call, position) => {
      if (position !== index || !call.manualFunction) return call;
      try { return output === undefined ? provideManualToolResult(call, call.manualOutput ?? "") : editManualToolResult(call, output); }
      catch (cause) { return { ...call, output: undefined, status: "pending", error: cause instanceof Error ? cause.message : "Invalid tool result." }; }
    }) } : item));
  }
  async function taskOperation(panel: Panel, task: AgentTask, method?: "GetTask" | "CancelTask", choices?: { call_id: string; approved: boolean }[]) {
    if (running || connectionChanged) return;
    const controller = new AbortController(), activeGeneration = ++generation.current; controllers.current.set(panel.id, controller); setRunning(true);
    try {
      const publish = (result: AgentRun) => {
        if (generation.current !== activeGeneration || controller.signal.aborted) return;
        const state = result.task?.state, failed = !!state && ["TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED"].includes(state);
        setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, agentRun: result, error: failed ? `Agent task ended in ${state}.` : "", status: failed ? "failed" : state === "TASK_STATE_COMPLETED" ? "complete" : "awaiting_task", history: item.history.map((turn, index) => index === item.history.length - 1 && turn.role === "assistant" ? { ...turn, content: result.text || state || "No text output" } : turn) } : item));
      };
      publish(await runAgentRequest(connection, choices ? agentApprovalRequest(panel.agent, task, choices, stream && !agentBackground, agentBackground) : agentTaskRequest(panel.agent, task, method!), controller.signal, publish));
    } catch (cause) { if (generation.current === activeGeneration) setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, error: cause instanceof Error ? cause.message : "Agent task operation failed." } : item)); }
    finally { if (controllers.current.get(panel.id) === controller) controllers.current.delete(panel.id); if (generation.current === activeGeneration) setRunning(false); }
  }

  function exportResults() {
    const rows = [["model", "status", "prompt", "response", "error", "input_tokens", "output_tokens", "latency_ms", "first_token_ms", "estimated_token_cost", "target_type", "agent_id", "task_id", "task_state", "latency_kind"]];
    const csv = rows[0].map(csvCell).join(",") + "\r\n" + panels.map((panel) => [panel.model, panel.status, panel.lastPrompt, panel.result?.text || panel.agentRun?.text || panel.pending, panel.error,
      panel.result?.usage?.prompt_tokens ?? panel.result?.usage?.input_tokens, panel.result?.usage?.completion_tokens ?? panel.result?.usage?.output_tokens,
      panel.result?.latencyMS ?? panel.agentRun?.latencyMS, panel.result?.firstTokenMS, estimateCost(panel.pricing, panel.result?.usage?.prompt_tokens ?? panel.result?.usage?.input_tokens, panel.result?.usage?.completion_tokens ?? panel.result?.usage?.output_tokens), panel.kind, panel.agent || undefined, panel.agentRun?.task?.id, panel.agentRun?.task?.state, panel.result ? "model_request" : panel.agentRun ? "agent_request" : undefined].map(csvCell).join(",")).join("\r\n") + "\r\n";
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    const link = document.createElement("a"); link.href = url; link.download = "ai-gateway-comparison.csv"; link.click(); URL.revokeObjectURL(url);
  }

  return <div className="playground-compare">
    <section className="playground-parameters-card playground-compare-connection" aria-label="Comparison connection">{connectionControls}</section>
    <div className="playground-compare-toolbar">
      <GravityThemeScope><Checkbox controlProps={{ "aria-label": "Sync settings across models" }} checked={sync} disabled={running} onUpdate={(value) => {
        setSync(value); if (value) setPanels((current) => current.map((panel) => ({ ...panel, settings: { ...current[0].settings }, instructions: current[0].instructions })));
      }}>Sync settings across models</Checkbox></GravityThemeScope>
      <GravityThemeScope><Checkbox controlProps={{ "aria-label": "Run comparison agents in background" }} checked={agentBackground} disabled={running} onUpdate={setAgentBackground}>Run agents in background</Checkbox></GravityThemeScope>
      {agentBackground && <p className="muted">Agent panels queue server tasks and require Refresh to observe completion or tool review. Model panels keep their selected streaming mode. Closing a local request does not cancel a queued task.</p>}
      <GravityThemeScope><Checkbox controlProps={{ "aria-label": "Stream comparison" }} checked={stream} disabled={running} onUpdate={setStream}>Stream responses</Checkbox></GravityThemeScope>
      <div className="playground-actions"><GatewayButton view="outlined" disabled={running || loadingAgents || connectionChanged} onClick={() => void loadAgents()}>Load authorized agents</GatewayButton><GatewayButton view="outlined" disabled={running || !panels.some((panel) => panel.lastPrompt)} onClick={exportResults}>Export results</GatewayButton><GatewayButton view="outlined" disabled={running} onClick={() => { setPanels((current) => current.map((panel) => ({ ...newPanel(panel.id, panel.model), kind: panel.kind, agent: panel.agent, settings: panel.settings, instructions: panel.instructions, pricing: panel.pricing, resources: panel.resources }))); setError(""); clearAttachments(); }}>Clear all chats</GatewayButton><GatewayButton view="outlined" disabled={running || panels.length >= 3} onClick={() => {
        const id = nextID.current++; setPanels((current) => [...current, { ...newPanel(id, models[current.length] || models[0] || ""), ...(sync ? { settings: { ...current[0].settings }, instructions: current[0].instructions } : {}) }]);
      }}>Add comparison</GatewayButton></div>
    </div>
    <p className="muted">Model generation settings can be synchronized. Tools, prompt policies and prices are configured independently for each model. Agents use saved server configuration and A2A task continuity; their usage and first-token timing are not reported by this protocol.</p>
    {agentsError && <p role="alert">{agentsError}</p>}{pendingApprovals && <p role="status">Resolve pending tools and agent tasks, or clear the affected conversation, before sending a new shared prompt.</p>}
    <div className="playground-comparison-panels" style={{ "--comparison-count": panels.length } as CSSProperties}>
      {panels.map((panel, index) => <section key={panel.id} className="playground-comparison-panel" aria-label={`Comparison ${index + 1}`}>
        <div className="playground-output-heading"><h2>{panel.kind === "model" ? "Model" : "Agent"} {index + 1}</h2><GatewayButton view="flat" aria-label={`Remove comparison ${index + 1}`} disabled={running || panels.length <= 1} onClick={() => setPanels((current) => current.filter((item) => item.id !== panel.id))}>Remove</GatewayButton></div>
        <GatewayButton view="outlined" disabled={running} onClick={() => setPanels((items) => items.map((item) => item.id === panel.id ? { ...newPanel(item.id, item.model), kind: item.kind, agent: item.agent, settings: item.settings, instructions: item.instructions, pricing: item.pricing, resources: item.resources } : item))}>Clear comparison {index + 1} chat</GatewayButton>
        <SelectControl label={`Comparison type ${index + 1}`} value={panel.kind} disabled={running} options={[{ value: "model", content: "Model" }, { value: "agent", content: "Saved agent" }]} onUpdate={(kind) => setPanels((items) => items.map((item) => item.id === panel.id ? { ...newPanel(item.id, kind === "model" ? models[index] || models[0] || "" : ""), kind } : item))} />
        {panel.kind === "model" ? <ModelControl label={`Model ${index + 1}`} value={panel.model} models={models} scope={connection} disabled={running} onUpdate={(model) => setPanels((current) => current.map((item) => item.id === panel.id ? { ...newPanel(item.id, model), settings: item.settings, instructions: item.instructions } : item))} /> : agents.length ? <SelectControl label={`Agent ${index + 1}`} value={panel.agent} disabled={running} options={[{ value: "", content: "Select an authorized agent" }, ...agents.map((item) => ({ value: item.id, content: `${item.name} · ${item.model}` }))]} onUpdate={(agent) => setPanels((items) => items.map((item) => item.id === panel.id ? { ...newPanel(item.id, agents.find((value) => value.id === agent)?.model || ""), kind: "agent", agent } : item))} /> : <TextControl label={`Agent ${index + 1}`} value={panel.agent} disabled={running} onUpdate={(agent) => setPanels((items) => items.map((item) => item.id === panel.id ? { ...newPanel(item.id, ""), kind: "agent", agent } : item))} placeholder="Load agents or enter an authorized agent ID" />}
        {panel.kind === "model" && <>
        <details className="playground-advanced"><summary>Model settings</summary>
          <AreaControl label={`Instructions ${index + 1}`} rows={2} value={panel.instructions} disabled={running} onUpdate={(value) => updateInstructions(panel.id, value)} />
          <TextControl label={`Temperature ${index + 1}`} type="number" controlProps={{ min: 0, max: 2, step: 0.1 }} value={panel.settings.temperature} disabled={running} placeholder="Provider default" onUpdate={(temperature) => settings(panel.id, { temperature })} />
          <TextControl label={`Max tokens ${index + 1}`} type="number" controlProps={{ min: 1, step: 1 }} value={panel.settings.maxTokens} disabled={running} onUpdate={(maxTokens) => settings(panel.id, { maxTokens })} />
          <TextControl label={`Top P ${index + 1}`} type="number" controlProps={{ min: 0, max: 1, step: 0.05 }} value={panel.settings.topP} disabled={running} placeholder="Provider default" onUpdate={(topP) => settings(panel.id, { topP })} />
          <SelectControl label={`Response format ${index + 1}`} value={panel.settings.responseFormat} disabled={running} options={[{ value: "text", content: "Text" }, { value: "json_object", content: "JSON object" }, { value: "json_schema", content: "JSON schema" }]} onUpdate={(responseFormat) => settings(panel.id, { responseFormat })} />
          {panel.settings.responseFormat === "json_schema" && <AreaControl label={`Output schema ${index + 1}`} rows={3} value={panel.settings.schema} disabled={running} onUpdate={(schema) => settings(panel.id, { schema })} />}
          <AreaControl label={`Advanced parameters ${index + 1}`} rows={3} value={panel.settings.advanced} disabled={running} onUpdate={(advanced) => settings(panel.id, { advanced })} />
        </details>
        <ResourceControls connection={connection} model={panel.model} endpoint="chat" value={panel.resources} disabled={running || connectionChanged || panel.calls.length > 0} onUpdate={(resources) => setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, resources } : item))} />
        <PricingControls label={` ${index + 1}`} disabled={running} value={panel.pricing} onUpdate={(pricing) => setPanels((current) => current.map((item) => item.id === panel.id ? { ...item, pricing } : item))} />
        </>}
        {panel.kind === "agent" && <p className="muted">Agent settings are saved on the server. {panel.agentRun && !panel.agentRun.task && "Stateless responses are independent requests."}</p>}
        {panel.historyDropped > 0 && <p className="muted">{panel.historyDropped} earlier turns removed to keep browser history bounded.</p>}
        <div className="playground-comparison-output" aria-live="polite">
          {!panel.history.length && !panel.pending && <p className="muted">Send the same prompt to compare responses.</p>}
          {panel.history.map((turn, turnIndex) => <article className={`playground-turn ${turn.role}`} key={turnIndex}><strong>{turn.role}</strong>{turn.role === "assistant" ? <MarkdownOutput text={contentText(turn.content) || contentText(turn.refusal)} /> : <pre>{contentText(turn.content) || (Array.isArray(turn.content) ? "Attachments only" : "No text output")}</pre>}{Array.isArray(turn.content) && <p className="muted">Attachments: {turn.content.map((part) => { const item = part as { type?: string; filename?: string }; return item.type === "input_file" ? item.filename : item.type === "image_url" ? "image" : ""; }).filter(Boolean).join(", ")}</p>}{turn.role === "assistant" && <><CopyOutput label={`Copy comparison ${index + 1} turn ${turnIndex + 1}`} text={contentText(turn.content) || contentText(turn.refusal)} />{contentText(turn.reasoning_content || turn.reasoning) && <details><summary>Reasoning</summary><pre>{contentText(turn.reasoning_content || turn.reasoning)}</pre></details>}<OutputDetails payload={turn} /></>}</article>)}
          {panel.pending && <article className="playground-turn assistant streaming"><strong>{panel.status === "running" ? "Streaming" : "Partial response"}</strong><MarkdownOutput text={panel.pending} /></article>}
          {panel.error && <p role="alert" className="form-error">{panel.error}</p>}
        </div>
        <ToolApprovals label={`Comparison ${index + 1} tool approvals`} continueLabel={`Continue comparison ${index + 1} with tool results`} calls={panel.calls} disabled={running || connectionChanged} onResultEdit={(position, output) => manualToolResult(panel, position, output)} onUseResult={(position) => manualToolResult(panel, position)} onExecute={(position) => void approveTool(panel, position)} onDecline={(position) => setPanels((items) => items.map((item) => item.id === panel.id ? { ...item, calls: item.calls.map((call, i) => i === position ? decideTool(call, false) : call) } : item))} onContinue={() => void continuePanel(panel)} />
        {panel.agentRun && <dl className="playground-metadata"><div><dt>Last request latency</dt><dd>{Math.round(panel.agentRun.latencyMS)} ms</dd></div><div><dt>Agent execution time</dt><dd>Not reported</dd></div><div><dt>Task status</dt><dd>{panel.agentRun.task?.state || "Stateless response"}</dd></div><div><dt>Tokens</dt><dd>Not reported</dd></div><div><dt>First token</dt><dd>Not reported</dd></div><div><dt>Finalized cost</dt><dd>See Usage &amp; spend</dd></div></dl>}
        {panel.agentRun?.task && <div className="playground-actions"><GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => void taskOperation(panel, panel.agentRun!.task!, "GetTask")}>Refresh comparison {index + 1} task</GatewayButton>{["TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED"].includes(panel.agentRun.task.state) && <GatewayButton view="outlined" disabled={running || connectionChanged} onClick={() => void taskOperation(panel, panel.agentRun!.task!, "CancelTask")}>Cancel comparison {index + 1} task</GatewayButton>}</div>}
        {panel.agentRun?.task && <AgentToolApprovals label={`Comparison ${index + 1} agent tool approvals`} task={panel.agentRun.task} disabled={running || connectionChanged} onContinue={(choices) => void taskOperation(panel, panel.agentRun!.task!, undefined, choices)} />}
        {panel.result && <dl className="playground-metadata">
          <div><dt>Input tokens</dt><dd>{panel.result.usage?.prompt_tokens ?? panel.result.usage?.input_tokens ?? "—"}</dd></div>
          <div><dt>Output tokens</dt><dd>{panel.result.usage?.completion_tokens ?? panel.result.usage?.output_tokens ?? "—"}</dd></div>
          <div><dt>Reasoning tokens</dt><dd>{panel.result.usage?.completion_tokens_details?.reasoning_tokens ?? panel.result.usage?.output_tokens_details?.reasoning_tokens ?? "—"}</dd></div>
          <div><dt>Latency</dt><dd>{Math.round(panel.result.latencyMS)} ms</dd></div>
          <div><dt>First token</dt><dd>{panel.result.firstTokenMS === undefined ? "—" : `${Math.round(panel.result.firstTokenMS)} ms`}</dd></div>
          <div><dt>Estimated token cost</dt><dd>{estimateCost(panel.pricing, panel.result.usage?.prompt_tokens ?? panel.result.usage?.input_tokens, panel.result.usage?.completion_tokens ?? panel.result.usage?.output_tokens)}</dd></div>
          <div><dt>Finalized cost</dt><dd>See Usage &amp; spend</dd></div>
        </dl>}
      </section>)}
    </div>
    <form className="playground-compare-composer" onSubmit={submit}>
      <label>Shared images or PDF<input ref={attachmentInput} aria-label="Comparison attachments" type="file" multiple accept="image/png,image/jpeg,image/gif,image/webp,application/pdf" disabled={running || reading || pendingApprovals} onChange={async (event) => {
        const files = [...(event.currentTarget.files || [])], current = ++attachmentGeneration.current; setAttachments([]); setReading(true);
        try { const loaded = await conversationAttachments(files); if (current === attachmentGeneration.current) { setAttachments(loaded); setError(""); } }
        catch (cause) { if (current === attachmentGeneration.current) { if (attachmentInput.current) attachmentInput.current.value = ""; setError(cause instanceof Error ? cause.message : "Attachment failed"); } }
        finally { if (current === attachmentGeneration.current) setReading(false); }
      }} /><span className="muted">Up to 5 files, 8 MiB total. Each selected model or agent receives the same attachments.</span></label>
      {attachments.length > 0 && <div className="playground-actions"><span>{attachments.map((item) => item.filename).join(", ")}</span><GatewayButton view="flat" disabled={running} onClick={clearAttachments}>Remove comparison attachments</GatewayButton></div>}
      <AreaControl label="Comparison prompt" rows={3} value={prompt} disabled={running || pendingApprovals} onUpdate={setPrompt} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); if (!running) event.currentTarget.form?.requestSubmit(); } }} placeholder="Send the same prompt to all models or agents" />
      <div className="playground-actions"><GatewayButton type="submit" disabled={running || reading || pendingApprovals || connectionChanged || (!prompt.trim() && !attachments.length) || panels.some((panel) => panel.kind === "model" ? !panel.model : !panel.agent)}>Compare models</GatewayButton>{running && <GatewayButton view="outlined" onClick={cancel}>Stop comparison</GatewayButton>}</div>
      {error && <p role="alert" className="form-error">{error}</p>}
    </form>
  </div>;
}
