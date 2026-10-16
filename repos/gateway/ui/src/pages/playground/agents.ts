import { csvCell } from "../../csv";
import type { Attachment } from "./endpointRequests";
import { optionalNumber, type PlaygroundConnection } from "./requests";
import { validateConversationFiles } from "./nativeConversation";
import { contentText } from "./runText";
import { exactObjectText, parseAgentJSON } from "./exactJSON";

export type AgentMCPTool = { server_id: string; tool_name: string };
export type AgentProfile = { id: string; name: string; description?: string; model: string; instructions_template_id?: string; instructions_configured: boolean; generation?: { temperature?: number; max_output_tokens?: number }; mcp_tools?: AgentMCPTool[]; tool_policy_id: string; allowed_tools: string[]; denied_tools?: string[]; approval_required?: string[]; max_tool_calls: number; max_iterations: number; tags?: string[]; enabled: boolean; execution_supported: boolean };
export type AgentPolicy = { id: string; name: string; enabled: boolean; allowed_tools: string[]; denied_tools?: string[]; approval_required?: string[]; max_tool_calls: number };
export type AgentDraft = { id: string; name: string; description: string; model: string; instructions: string; template: string; temperature: string; maxTokens: string; policy: string; iterations: string; tools: AgentMCPTool[]; tags: string; enabled: boolean };
export const emptyAgentDraft: AgentDraft = { id: "", name: "", description: "", model: "", instructions: "", template: "", temperature: "", maxTokens: "", policy: "", iterations: "1", tools: [], tags: "", enabled: true };
const safeID = /^[a-zA-Z0-9._-]{1,128}$/;
export function agentDraft(profile: AgentProfile, instructions: string): AgentDraft {
  return { id: profile.id, name: profile.name, description: profile.description || "", model: profile.model, instructions, template: profile.instructions_template_id || "", temperature: profile.generation?.temperature === undefined ? "" : String(profile.generation.temperature), maxTokens: profile.generation?.max_output_tokens === undefined ? "" : String(profile.generation.max_output_tokens), policy: profile.tool_policy_id, iterations: String(profile.max_iterations), tools: (profile.mcp_tools || []).map((tool) => ({ ...tool })), tags: (profile.tags || []).join(", "), enabled: profile.enabled };
}
export function agentBody(draft: AgentDraft) {
  if (!safeID.test(draft.id) || !draft.name.trim() || new TextEncoder().encode(draft.name.trim()).length > 256 || !draft.model.trim() || new TextEncoder().encode(draft.model.trim()).length > 256 || !safeID.test(draft.policy)) throw new Error("Enter a valid agent ID, name, model and tool policy.");
  if (new TextEncoder().encode(draft.instructions).length > 65536 || draft.instructions.includes("\0")) throw new Error("Agent instructions must contain at most 64 KiB and no NUL characters.");
  if (new TextEncoder().encode(draft.description.trim()).length > 1024 || draft.template.length > 256) throw new Error("Agent description or template ID is too long.");
  if (draft.instructions && draft.template) throw new Error("Clear the instruction template before entering inline instructions.");
  const temperature = optionalNumber(draft.temperature, "Agent temperature", 0, 2), tokens = optionalNumber(draft.maxTokens, "Agent maximum output tokens", 1, 1000000, true);
  const iterations = optionalNumber(draft.iterations, "Agent maximum iterations", 1, 50, true);
  if (iterations === undefined) throw new Error("Enter the agent maximum iterations.");
  validateAgentMCPTools(draft.tools);
  const tags = [...new Set(draft.tags.split(",").map((value) => value.trim()).filter(Boolean))];
  if (tags.length > 256 || tags.some((value) => new TextEncoder().encode(value).length > 512 || value.includes("\0"))) throw new Error("Agent tags are too large or invalid.");
  return { name: draft.name.trim(), description: draft.description.trim(), model: draft.model.trim(), instructions: draft.instructions, instructions_template_id: draft.template.trim(), generation: { ...(temperature === undefined ? {} : { temperature }), ...(tokens === undefined ? {} : { max_output_tokens: tokens }) }, tool_policy_id: draft.policy, mcp_tools: draft.tools.map((tool) => ({ ...tool })), max_iterations: iterations, tags, enabled: draft.enabled };
}
export function validateAgentMCPTools(tools: AgentMCPTool[]) {
  if (tools.length > 32 || tools.some((tool) => !safeID.test(tool.server_id) || !tool.tool_name || tool.tool_name.trim() !== tool.tool_name || new TextEncoder().encode(tool.tool_name).length > 256 || /[\x00-\x1f\x7f]/.test(tool.tool_name)) || new Set(tools.map((tool) => JSON.stringify([tool.server_id, tool.tool_name]))).size !== tools.length) throw new Error("Select up to 32 distinct MCP tools with valid server IDs and names.");
}
export type AgentApproval = { id: string; calls: { id: string; server: string; tool: string; arguments: Record<string, unknown>; rawArguments?: string }[] };
export type AgentTask = { id: string; contextID: string; state: string; approval?: AgentApproval };
export type AgentRun = { text: string; task?: AgentTask; latencyMS: number; response: Record<string, unknown> };
function object(value: unknown): Record<string, unknown> | undefined { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined; }
export function agentRequest(agent: string, prompt: string, task?: AgentTask, attachments: Attachment[] = [], streaming = false, background = false) {
  if (streaming && background) throw new Error("Background agent execution cannot stream the same request.");
  if (!safeID.test(agent)) throw new Error("Select a valid agent ID.");
  if ((!prompt.trim() && !attachments.length) || new TextEncoder().encode(prompt).length > 1024 * 1024) throw new Error("Enter an agent prompt up to 1 MiB.");
  if (task && (!safeID.test(task.id) || !safeID.test(task.contextID) || task.state !== "TASK_STATE_COMPLETED")) throw new Error("Start a new conversation or resolve the current agent task before continuing.");
  validateConversationFiles(attachments);
  const body = { jsonrpc: "2.0", id: crypto.randomUUID(), method: streaming ? "SendStreamingMessage" : "SendMessage", params: { tenant: agent, message: { messageId: crypto.randomUUID(), role: "ROLE_USER", ...(task ? { taskId: task.id, contextId: task.contextID } : {}), parts: [...(prompt ? [{ text: prompt }] : []), ...attachments.map((item) => ({ raw: item.data_base64, mediaType: item.media_type, filename: item.filename }))] }, configuration: { acceptedOutputModes: ["text/plain"], ...(background ? { returnImmediately: true } : {}) } } };
  if (new TextEncoder().encode(JSON.stringify(body)).length > 24 * 1024 * 1024) throw new Error("Request exceeds the 24 MiB Playground limit.");
  return { path: `/a2a/${encodeURIComponent(agent)}`, body, headers: { "A2A-Version": "1.0" } };
}
export function agentTaskRequest(agent: string, task: AgentTask, method: "GetTask" | "CancelTask") {
  if (!safeID.test(agent) || !safeID.test(task.id) || !safeID.test(task.contextID)) throw new Error("Invalid agent task ID.");
  return { path: `/a2a/${encodeURIComponent(agent)}`, body: { jsonrpc: "2.0", id: crypto.randomUUID(), method, params: { tenant: agent, id: task.id } }, headers: { "A2A-Version": "1.0" }, expectedTask: { id: task.id, contextID: task.contextID } };
}
export function agentApprovalRequest(agent: string, task: AgentTask, choices: { call_id: string; approved: boolean }[], streaming = false, background = false) {
  if (streaming && background) throw new Error("Background agent execution cannot stream the same request.");
  if (!safeID.test(agent) || !safeID.test(task.id) || !safeID.test(task.contextID) || task.state !== "TASK_STATE_INPUT_REQUIRED" || !task.approval || !safeID.test(task.approval.id) || choices.length !== task.approval.calls.length || new Set(choices.map((choice) => choice.call_id)).size !== choices.length || choices.some((choice) => typeof choice.approved !== "boolean" || !task.approval!.calls.some((call) => call.id === choice.call_id))) throw new Error("Choose approve or decline for every pending agent tool call.");
  return { path: `/a2a/${encodeURIComponent(agent)}`, body: { jsonrpc: "2.0", id: crypto.randomUUID(), method: streaming ? "SendStreamingMessage" : "SendMessage", params: { tenant: agent, ...(background ? { configuration: { returnImmediately: true } } : {}), message: { messageId: crypto.randomUUID(), taskId: task.id, contextId: task.contextID, role: "ROLE_USER", parts: [{ text: "Review decisions for pending agent tools" }], metadata: { ai_gateway_tool_approval: { approval_id: task.approval.id, choices: choices.map((choice) => ({ ...choice })) } } } } }, headers: { "A2A-Version": "1.0" }, expectedTask: { id: task.id, contextID: task.contextID } };
}
function agentApproval(task: Record<string, unknown>, state: string): AgentApproval | undefined {
  const message = object(object(task.status)?.message), raw = object(message?.metadata)?.ai_gateway_tool_approval;
  if (raw === undefined) return;
  const metadata = object(raw), calls = metadata?.calls;
  if (state !== "TASK_STATE_INPUT_REQUIRED" || message?.role !== "ROLE_AGENT" || message.taskId !== task.id || message.contextId !== task.contextId || typeof metadata?.approval_id !== "string" || !safeID.test(metadata.approval_id) || !Array.isArray(calls) || !calls.length || calls.length > 32) throw new Error("Gateway returned an invalid agent approval.");
  const parsed = calls.map((value) => { const call = object(value), args = object(call?.arguments); if (typeof call?.call_id !== "string" || !call.call_id || new TextEncoder().encode(call.call_id).length > 128 || typeof call.server_id !== "string" || !safeID.test(call.server_id) || typeof call.tool_name !== "string" || !call.tool_name || new TextEncoder().encode(call.tool_name).length > 256 || !args) throw new Error("Gateway returned an invalid agent approval call."); return { id: call.call_id, server: call.server_id, tool: call.tool_name, arguments: args, rawArguments: exactObjectText(args) }; });
  if (new Set(parsed.map((call) => call.id)).size !== parsed.length) throw new Error("Gateway returned repeated agent approval calls.");
  return { id: metadata.approval_id, calls: parsed };
}
type AgentRequest = { path: string; body: Record<string, unknown>; headers?: Record<string, string>; expectedTask?: Pick<AgentTask, "id" | "contextID"> };
function agentResponse(request: AgentRequest, payload: Record<string, unknown>, start: number): AgentRun {
  const params = object(request.body.params)!;
  if (!object(payload) || payload.jsonrpc !== "2.0" || payload.id !== request.body.id) throw new Error("Gateway returned an invalid agent response.");
  const error = object(payload.error); if (error) throw new Error(typeof error.message === "string" ? error.message : "Agent request failed.");
  const result = object(payload.result), task = object(result?.task) || (["GetTask", "CancelTask"].includes(String(request.body.method)) && object(result?.status) ? result : undefined), message = object(result?.message);
  if (task) {
    const state = object(task.status)?.state;
    if (typeof task.id !== "string" || !safeID.test(task.id) || typeof task.contextId !== "string" || !safeID.test(task.contextId) || typeof state !== "string" || !["TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED"].includes(state)) throw new Error("Gateway returned an invalid agent task.");
    const continuation = object(params.message);
    if (("id" in params && params.id !== task.id) || (continuation?.taskId !== undefined && (continuation.taskId !== task.id || continuation.contextId !== task.contextId)) || (request.expectedTask && (request.expectedTask.id !== task.id || request.expectedTask.contextID !== task.contextId))) throw new Error("Gateway returned a different agent task.");
    const artifacts = Array.isArray(task.artifacts) ? task.artifacts : [];
    const latest = object(artifacts.at(-1));
    return { text: contentText(latest?.parts) || contentText(object(object(task.status)?.message)?.parts), task: { id: task.id, contextID: task.contextId, state, approval: agentApproval(task, state) }, latencyMS: performance.now() - start, response: payload };
  }
  if (!message || !Array.isArray(message.parts)) throw new Error("Gateway returned no agent message or task.");
  return { text: contentText(message.parts), latencyMS: performance.now() - start, response: payload };
}
export async function runAgentRequest(connection: PlaygroundConnection, request: AgentRequest, signal: AbortSignal, onUpdate?: (result: AgentRun) => void): Promise<AgentRun> {
  const active = () => { if (signal.aborted) throw new DOMException("Request cancelled", "AbortError"); };
  active();
  const params = object(request.body.params);
  if (request.body.jsonrpc !== "2.0" || typeof request.body.id !== "string" || !params || !["SendMessage", "SendStreamingMessage", "GetTask", "CancelTask"].includes(String(request.body.method))) throw new Error("Invalid agent RPC request.");
  const start = performance.now();
  const options = { method: "POST", body: request.body, headers: request.headers, signal, maximumResponseBytes: 4 * 1024 * 1024, parseJSON: parseAgentJSON };
  const path = connection.path(request.path);
  if (request.body.method !== "SendStreamingMessage") {
    const payload = await connection.client.request<Record<string, unknown>>(path, options);
    active(); return agentResponse(request, payload, start);
  }
  let task: Record<string, unknown> | undefined, statusSeen = false, terminal = false, eventCount = 0;
  const payload = () => ({ jsonrpc: "2.0", id: request.body.id, result: { task } });
  const parts = (value: unknown) => {
    if (!Array.isArray(value) || value.length > 1024 || value.some((part) => typeof object(part)?.text !== "string")) throw new Error("Gateway returned an invalid text artifact.");
    return value;
  };
  const result = await connection.client.stream<Record<string, unknown>>(path, options, (event) => {
    active();
    if (++eventCount > 16384 || terminal) throw new Error("Gateway returned excessive events or data after the final agent status.");
    let envelope: Record<string, unknown> | undefined;
    try { envelope = object(parseAgentJSON(event.data)); } catch { throw new Error("Malformed agent stream event."); }
    if (!envelope || envelope.jsonrpc !== "2.0" || envelope.id !== request.body.id) throw new Error("Gateway returned an invalid agent response.");
    const error = object(envelope.error);
    if (error) throw new Error(typeof error.message === "string" ? error.message : "Agent stream failed.");
    const result = object(envelope.result), initial = object(result?.task), artifact = object(result?.artifactUpdate), status = object(result?.statusUpdate);
    if ([initial, artifact, status].filter(Boolean).length !== 1) throw new Error("Gateway returned an invalid agent stream event.");
    if (initial) {
      if (task) throw new Error("Gateway returned a repeated agent task.");
      const artifacts = initial.artifacts;
      if (artifacts !== undefined && (!Array.isArray(artifacts) || artifacts.length > 128)) throw new Error("Gateway returned invalid agent artifacts.");
      const ids = new Set<string>();
      for (const item of Array.isArray(artifacts) ? artifacts : []) {
        const artifact = object(item);
        if (typeof artifact?.artifactId !== "string" || !safeID.test(artifact.artifactId) || ids.has(artifact.artifactId)) throw new Error("Gateway returned invalid agent artifacts.");
        ids.add(artifact.artifactId); parts(artifact.parts);
      }
      task = { ...initial };
    } else {
      const update = artifact || status!;
      if (!task || update.taskId !== task.id || update.contextId !== task.contextId) throw new Error("Gateway returned a different agent task.");
      if (artifact) {
        const item = object(artifact.artifact);
        if (typeof item?.artifactId !== "string" || !safeID.test(item.artifactId) || typeof artifact.append !== "boolean" || typeof artifact.lastChunk !== "boolean") throw new Error("Gateway returned an invalid agent artifact update.");
        const incoming = parts(item.parts), artifacts = Array.isArray(task.artifacts) ? [...task.artifacts] : [];
        const index = artifacts.findIndex((value) => object(value)?.artifactId === item.artifactId);
        if (artifact.append && index < 0) throw new Error("Agent artifact append references an unknown artifact.");
        const next = artifact.append ? { ...item, parts: [{ text: contentText(object(artifacts[index])?.parts) + contentText(incoming) }] } : { ...item, parts: incoming };
        if (index < 0) artifacts.push(next); else artifacts[index] = next;
        if (artifacts.length > 128) throw new Error("Gateway returned too many agent artifacts.");
        task = { ...task, artifacts }; statusSeen = false;
      } else {
        if (!object(status!.status)) throw new Error("Gateway returned an invalid agent status update.");
        task = { ...task, status: status!.status }; statusSeen = true;
      }
    }
    const current = agentResponse(request, payload(), start);
    if (status) terminal = !["TASK_STATE_WORKING", "TASK_STATE_SUBMITTED"].includes(current.task!.state);
    onUpdate?.(current);
  }, true);
  active();
  if (!result.streamed) return agentResponse(request, result.data, start);
  if (!task || !statusSeen) throw new Error("Agent stream ended before its final status. Refresh the known task before repeating execution.");
  return agentResponse(request, payload(), start);
}
export type AgentBatchResult = { index: number; prompt: string; status: "completed" | "failed" | "cancelled" | "pending"; text?: string; error?: string; task?: AgentTask; latencyMS?: number };
export function agentBatchStatus(task?: AgentTask): AgentBatchResult["status"] {
  if (!task || task.state === "TASK_STATE_COMPLETED") return "completed";
  if (task.state === "TASK_STATE_CANCELED") return "cancelled";
  return ["TASK_STATE_INPUT_REQUIRED", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_AUTH_REQUIRED"].includes(task.state) ? "pending" : "failed";
}
export function agentBatchPrompts(value: string) {
  const prompts = value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
  if (!prompts.length || prompts.length > 20 || prompts.some((item) => new TextEncoder().encode(item).length > 65536)) throw new Error("Enter 1–20 prompts, one per line, up to 64 KiB each.");
  return prompts;
}
export async function runAgentBatch(connection: PlaygroundConnection, agent: string, prompts: string[], signal: AbortSignal, onResult: (result: AgentBatchResult) => void, streaming = false, background = false) {
  if (!prompts.length || prompts.length > 20 || prompts.some((prompt) => !prompt.trim() || new TextEncoder().encode(prompt).length > 65536)) throw new Error("Choose 1–20 prompts up to 64 KiB each.");
  if (!safeID.test(agent)) throw new Error("Select a valid agent ID.");
  let cursor = 0;
  async function worker() {
    while (cursor < prompts.length) {
      const index = cursor++, prompt = prompts[index], start = performance.now();
      if (signal.aborted) { onResult({ index, prompt, status: "cancelled" }); continue; }
      let observed: AgentRun | undefined;
      const publish = (result: AgentRun) => {
        observed = result;
        const status = agentBatchStatus(result.task);
        onResult({ index, prompt, status, text: result.text, task: result.task, error: status === "failed" ? `Task ended in ${result.task?.state}.` : undefined, latencyMS: result.latencyMS });
      };
      try {
        const result = await runAgentRequest(connection, agentRequest(agent, prompt, undefined, [], streaming, background), signal, publish);
        const status = agentBatchStatus(result.task);
        onResult({ index, prompt, status, text: result.text, task: result.task, error: status === "failed" ? `Task ended in ${result.task?.state}.` : undefined, latencyMS: result.latencyMS });
      } catch (cause) { onResult({ index, prompt, text: observed?.text, task: observed?.task, status: signal.aborted ? "cancelled" : "failed", error: signal.aborted ? undefined : cause instanceof Error ? cause.message : "Agent test failed.", latencyMS: performance.now() - start }); }
    }
  }
  await Promise.all(Array.from({ length: Math.min(2, prompts.length) }, () => worker()));
}
export function agentBatchCSV(results: AgentBatchResult[]) {
  return [["index", "prompt", "status", "output", "error", "task_id", "task_state", "latency_ms", "latency_kind"], ...[...results].sort((a, b) => a.index - b.index).map((item) => [item.index + 1, item.prompt, item.status, item.text, item.error, item.task?.id, item.task?.state, item.latencyMS, item.latencyMS === undefined ? undefined : "agent_request"])].map((row) => row.map(csvCell).join(",")).join("\r\n") + "\r\n";
}
