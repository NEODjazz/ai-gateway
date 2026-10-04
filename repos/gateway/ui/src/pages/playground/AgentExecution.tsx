import { useEffect, useRef, useState, type FormEvent } from "react";
import { Checkbox } from "@gravity-ui/uikit";
import { GravityThemeScope } from "../../components/GravityThemeScope";
import { GatewayButton } from "../../components/GatewayButton";
import { AreaControl } from "./Controls";
import { CodeDialog } from "./CodeDialog";
import { CopyOutput } from "./OutputDetails";
import { retainConversation } from "./attachments";
import { agentApprovalRequest, agentBatchCSV, agentBatchStatus, agentBatchPrompts, agentRequest, agentTaskRequest, runAgentBatch, runAgentRequest, type AgentBatchResult, type AgentProfile, type AgentRun } from "./agents";
import type { PlaygroundConnection } from "./requests";

import { AgentToolApprovals } from "./AgentToolApprovals";

type Turn = { role: "user" | "assistant"; content: string };
export function AgentExecution({ connection, profile, disabled, active, tab }: { connection: PlaygroundConnection; profile: AgentProfile; disabled: boolean; active: boolean; tab: "chat" | "batch" | "connect" }) {
  const [prompt, setPrompt] = useState(""), [history, setHistory] = useState<Turn[]>([]), [dropped, setDropped] = useState(0);
  const [result, setResult] = useState<AgentRun>(), [error, setError] = useState(""), [running, setRunning] = useState(false);
  const [batchPrompts, setBatchPrompts] = useState(""), [batchResults, setBatchResults] = useState<AgentBatchResult[]>([]), [batchCount, setBatchCount] = useState(0);
  const [streaming, setStreaming] = useState(false);
  const [code, setCode] = useState<ReturnType<typeof agentRequest>>();
  const abort = useRef<AbortController | undefined>(undefined), epoch = useRef(0);
  function stop() { abort.current?.abort(); }
  function clear() { epoch.current++; stop(); abort.current = undefined; setRunning(false); setHistory([]); setDropped(0); setResult(undefined); setError(""); setCode(undefined); }
  useEffect(() => () => { epoch.current++; stop(); }, []);
  useEffect(() => { if (!active || disabled) { stop(); setCode(undefined); } }, [active, disabled]);
  useEffect(() => { stop(); setCode(undefined); setError(""); }, [tab]);
  useEffect(() => { clear(); setPrompt(""); setBatchPrompts(""); setBatchResults([]); setBatchCount(0); }, [connection, profile]);
  const blocked = disabled || running || !profile.execution_supported || !profile.enabled;
  const unresolved = result?.task && result.task.state !== "TASK_STATE_COMPLETED";
  async function execute(event?: FormEvent, method?: "GetTask" | "CancelTask", choices?: { call_id: string; approved: boolean }[]) {
    event?.preventDefault(); if (blocked) return;
    let request: ReturnType<typeof agentRequest> | ReturnType<typeof agentTaskRequest> | ReturnType<typeof agentApprovalRequest>;
    try { request = choices && result?.task ? agentApprovalRequest(profile.id, result.task, choices, streaming) : method && result?.task ? agentTaskRequest(profile.id, result.task, method) : agentRequest(profile.id, prompt, result?.task, [], streaming); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid agent request."); return; }
    const controller = new AbortController(); abort.current = controller; const current = ++epoch.current; setRunning(true); setError("");
    try {
      const publish = (response: AgentRun) => {
        if (current !== epoch.current || controller.signal.aborted) return;
        setResult(response);
        const text = response.text || response.task?.state || "No text output";
        if (!method && !choices) {
          const retained = retainConversation<Turn>([...history, { role: "user", content: prompt }, { role: "assistant", content: text }]);
          setHistory(retained.turns); setDropped(dropped + retained.dropped);
        } else setHistory(history.map((turn, index) => index === history.length - 1 && turn.role === "assistant" ? { ...turn, content: text } : turn));
      };
      const response = await runAgentRequest(connection, request, controller.signal, publish);
      if (current !== epoch.current || controller.signal.aborted) return;
      publish(response);
      if (!method && !choices) setPrompt("");
    } catch (cause) { if (current === epoch.current) setError(controller.signal.aborted ? "Request cancelled. Server execution may already have completed; check the task before repeating it." : cause instanceof Error ? cause.message : "Agent execution failed."); }
    finally { if (current === epoch.current) { setRunning(false); abort.current = undefined; } }
  }
  async function batch() {
    if (blocked) return;
    let prompts: string[];
    try { prompts = agentBatchPrompts(batchPrompts); } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid prompts."); return; }
    const controller = new AbortController(); abort.current = controller; const current = ++epoch.current; setRunning(true); setError(""); setBatchCount(prompts.length); setBatchResults([]);
    try { await runAgentBatch(connection, profile.id, prompts, controller.signal, (item) => { if (current === epoch.current) setBatchResults((items) => [...items.filter((value) => value.index !== item.index), item]); }, streaming); }
    catch (cause) { if (current === epoch.current) setError(cause instanceof Error ? cause.message : "Agent tests failed."); }
    finally { if (current === epoch.current) { setRunning(false); abort.current = undefined; } }
  }
  async function batchTask(item: AgentBatchResult, method?: "GetTask" | "CancelTask", choices?: { call_id: string; approved: boolean }[]) {
    if (blocked || !item.task) return;
    const controller = new AbortController(); abort.current = controller; const current = ++epoch.current; setRunning(true); setError("");
    try {
      const request = choices ? agentApprovalRequest(profile.id, item.task, choices, streaming) : agentTaskRequest(profile.id, item.task, method!);
      const publish = (response: AgentRun) => {
        if (current !== epoch.current || controller.signal.aborted) return;
        const status = agentBatchStatus(response.task);
        setBatchResults((items) => items.map((value) => value.index === item.index ? { ...value, text: response.text, task: response.task, latencyMS: response.latencyMS, status, error: status === "failed" ? `Task ended in ${response.task?.state}.` : undefined } : value));
      };
      publish(await runAgentRequest(connection, request, controller.signal, publish));
    } catch (cause) { if (current === epoch.current) setError(cause instanceof Error ? cause.message : "Could not resolve agent test."); }
    finally { if (current === epoch.current) { setRunning(false); abort.current = undefined; } }
  }
  function getCode() {
    try { setCode(agentRequest(profile.id, tab === "connect" ? "Your message" : prompt || "Your message", tab === "connect" ? undefined : result?.task, [], streaming)); setError(""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Could not generate code."); }
  }
  function exportResults() {
    const url = URL.createObjectURL(new Blob([agentBatchCSV(batchResults)], { type: "text/csv;charset=utf-8" }));
    const link = document.createElement("a"); link.href = url; link.download = "ai-gateway-agent-results.csv"; link.click(); URL.revokeObjectURL(url);
  }
  return <section className="playground-conversation-card playground-agent-execution" aria-label="Saved agent execution">
    <div className="playground-output-heading"><div><h2>{tab === "chat" ? "Agent conversation" : tab === "batch" ? "Agent batch test" : "Connect to agent"}</h2><p className="muted">Saved agent: {profile.name} · {profile.model}</p></div>{tab === "chat" && <GatewayButton view="outlined" onClick={clear}>New agent conversation</GatewayButton>}</div>
    <GravityThemeScope><Checkbox controlProps={{ "aria-label": "Stream agent task" }} checked={streaming} disabled={blocked} onUpdate={setStreaming}>Stream task updates</Checkbox></GravityThemeScope>
    {streaming && <p className="muted">Task streaming reports status and text artifacts. Token usage and first-token timing are unavailable.</p>}
    {disabled && <p className="muted">Save agent and apply connection changes before execution.</p>}
    {(!profile.execution_supported || !profile.enabled) && <p role="alert">This saved agent is disabled or has an instruction template that cannot execute through A2A.</p>}
    {tab === "chat" && <><section className="playground-transcript" aria-label="Agent conversation history">{history.map((turn, index) => <article key={index} className={`playground-turn ${turn.role}`}><strong>{turn.role === "user" ? "User" : "Agent"}</strong><pre>{turn.content}</pre>{turn.role === "assistant" && <CopyOutput label={`Copy agent answer ${index + 1}`} text={turn.content} />}</article>)}</section>
      {dropped > 0 && <p className="muted">{dropped} earlier turns removed from the browser display.</p>}
      {result && <p role="status">{result.task ? `Task ${result.task.id} · ${result.task.state}` : "Stateless agent response: each request is independent."} · {Math.round(result.latencyMS)} ms</p>}
      {result?.task && <div className="playground-actions"><GatewayButton view="outlined" disabled={blocked} onClick={() => void execute(undefined, "GetTask")}>Refresh agent task</GatewayButton>{["TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED"].includes(result.task.state) && <GatewayButton view="outlined" disabled={blocked} onClick={() => void execute(undefined, "CancelTask")}>Cancel agent task</GatewayButton>}</div>}
      {result?.task && <AgentToolApprovals task={result.task} disabled={blocked} onContinue={(choices) => void execute(undefined, undefined, choices)} />}
      <form className="playground-agent-prompt" onSubmit={(event) => void execute(event)}><AreaControl label="Agent prompt" rows={4} value={prompt} disabled={blocked || !!unresolved} onUpdate={setPrompt} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} /><div className="playground-actions"><GatewayButton view="outlined" disabled={blocked || !!unresolved} onClick={getCode}>Get agent request code</GatewayButton><GatewayButton type="submit" disabled={blocked || !!unresolved || !prompt.trim()}>Send to agent</GatewayButton></div></form></>}
    {tab === "batch" && <><p className="muted">Runs saved configuration with real inference and normal billing. Each prompt starts an independent conversation. Up to 20 prompts, two requests at a time.</p><AreaControl label="Agent batch prompts" rows={6} value={batchPrompts} disabled={blocked} onUpdate={setBatchPrompts} placeholder="One prompt per line" /><div className="playground-actions"><GatewayButton view="outlined" disabled={running || !batchResults.length} onClick={exportResults}>Export agent results</GatewayButton><GatewayButton disabled={blocked || !batchPrompts.trim()} onClick={() => void batch()}>Run agent tests</GatewayButton></div><p role="status">{batchResults.length}/{batchCount} responses received · {batchResults.filter((item) => item.status === "completed").length} completed · {batchResults.filter((item) => item.status === "pending").length} pending · {batchResults.filter((item) => item.status === "failed").length} failed · {batchResults.filter((item) => item.status === "cancelled").length} cancelled</p>{[...batchResults].sort((a, b) => a.index - b.index).map((item) => <article key={item.index} className="playground-turn"><strong>Test {item.index + 1} · {item.status} · {Math.round(item.latencyMS)} ms</strong><pre>{item.prompt}</pre>{item.text && <pre>{item.text}</pre>}{item.error && <p role="alert">{item.error}</p>}{item.task && <><p>Task {item.task.id} · {item.task.state}</p><div className="playground-actions"><GatewayButton view="outlined" disabled={blocked} onClick={() => void batchTask(item, "GetTask")}>Refresh test {item.index + 1} task</GatewayButton>{["TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED"].includes(item.task.state) && <GatewayButton view="outlined" disabled={blocked} onClick={() => void batchTask(item, "CancelTask")}>Cancel test {item.index + 1} task</GatewayButton>}</div><AgentToolApprovals label={`Test ${item.index + 1} tool approvals`} task={item.task} disabled={blocked} onContinue={(choices) => void batchTask(item, undefined, choices)} /></>}</article>)}</>}
    {tab === "connect" && <><p>Send authenticated JSON-RPC requests to <code>/a2a/{profile.id}</code> with <code>A2A-Version: 1.0</code>. The server applies the saved model, instructions and generation settings.</p><p className="muted">When a completed task is returned, continue with its task and context IDs. A stateless message does not provide conversation continuity. Examples omit credentials and saved instructions.</p><GatewayButton disabled={disabled} onClick={getCode}>Get connection code</GatewayButton></>}
    {running && <GatewayButton view="outlined" onClick={stop}>Stop agent request</GatewayButton>}
    {error && <p role="alert" className="form-error">{error}</p>}
    {code && <CodeDialog {...code} baseURL={connection.baseURL} onClose={() => setCode(undefined)} />}
  </section>;
}
