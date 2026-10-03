import { csvCell } from "../../csv";
import type { Attachment } from "./endpointRequests";
import { optionalNumber, type PlaygroundConnection } from "./requests";
import { contentText } from "./runText";

export type AgentProfile = { id: string; name: string; description?: string; model: string; instructions_template_id?: string; instructions_configured: boolean; generation?: { temperature?: number; max_output_tokens?: number }; tool_policy_id: string; allowed_tools: string[]; denied_tools?: string[]; approval_required?: string[]; max_tool_calls: number; max_iterations: number; tags?: string[]; enabled: boolean; execution_supported: boolean };
export type AgentPolicy = { id: string; name: string; enabled: boolean; allowed_tools: string[]; denied_tools?: string[]; approval_required?: string[]; max_tool_calls: number };
export type AgentDraft = { id: string; name: string; description: string; model: string; instructions: string; template: string; temperature: string; maxTokens: string; policy: string; iterations: string; tags: string; enabled: boolean };
export const emptyAgentDraft: AgentDraft = { id: "", name: "", description: "", model: "", instructions: "", template: "", temperature: "", maxTokens: "", policy: "", iterations: "1", tags: "", enabled: true };
const safeID = /^[a-zA-Z0-9._-]{1,128}$/;
export function agentDraft(profile: AgentProfile, instructions: string): AgentDraft {
  return { id: profile.id, name: profile.name, description: profile.description || "", model: profile.model, instructions, template: profile.instructions_template_id || "", temperature: profile.generation?.temperature === undefined ? "" : String(profile.generation.temperature), maxTokens: profile.generation?.max_output_tokens === undefined ? "" : String(profile.generation.max_output_tokens), policy: profile.tool_policy_id, iterations: String(profile.max_iterations), tags: (profile.tags || []).join(", "), enabled: profile.enabled };
}
export function agentBody(draft: AgentDraft) {
  if (!safeID.test(draft.id) || !draft.name.trim() || new TextEncoder().encode(draft.name.trim()).length > 256 || !draft.model.trim() || new TextEncoder().encode(draft.model.trim()).length > 256 || !safeID.test(draft.policy)) throw new Error("Enter a valid agent ID, name, model and tool policy.");
  if (new TextEncoder().encode(draft.instructions).length > 65536 || draft.instructions.includes("\0")) throw new Error("Agent instructions must contain at most 64 KiB and no NUL characters.");
  if (new TextEncoder().encode(draft.description.trim()).length > 1024 || draft.template.length > 256) throw new Error("Agent description or template ID is too long.");
  if (draft.instructions && draft.template) throw new Error("Clear the instruction template before entering inline instructions.");
  const temperature = optionalNumber(draft.temperature, "Agent temperature", 0, 2), tokens = optionalNumber(draft.maxTokens, "Agent maximum output tokens", 1, 1000000, true);
  const iterations = optionalNumber(draft.iterations, "Agent maximum iterations", 1, 50, true);
  if (iterations === undefined) throw new Error("Enter the agent maximum iterations.");
  const tags = [...new Set(draft.tags.split(",").map((value) => value.trim()).filter(Boolean))];
  if (tags.length > 256 || tags.some((value) => new TextEncoder().encode(value).length > 512 || value.includes("\0"))) throw new Error("Agent tags are too large or invalid.");
  return { name: draft.name.trim(), description: draft.description.trim(), model: draft.model.trim(), instructions: draft.instructions, instructions_template_id: draft.template.trim(), generation: { ...(temperature === undefined ? {} : { temperature }), ...(tokens === undefined ? {} : { max_output_tokens: tokens }) }, tool_policy_id: draft.policy, max_iterations: iterations, tags, enabled: draft.enabled };
}
export type AgentTask = { id: string; contextID: string; state: string };
export type AgentRun = { text: string; task?: AgentTask; latencyMS: number; response: Record<string, unknown> };
function object(value: unknown): Record<string, unknown> | undefined { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined; }
export function agentRequest(agent: string, prompt: string, task?: AgentTask, attachments: Attachment[] = []) {
  if (!safeID.test(agent)) throw new Error("Select a valid agent ID.");
  if ((!prompt.trim() && !attachments.length) || new TextEncoder().encode(prompt).length > 1024 * 1024) throw new Error("Enter an agent prompt up to 1 MiB.");
  if (task && (!safeID.test(task.id) || !safeID.test(task.contextID) || task.state !== "TASK_STATE_COMPLETED")) throw new Error("Start a new conversation or resolve the current agent task before continuing.");
  if (attachments.length > 5 || attachments.some((item) => !["image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"].includes(item.media_type))) throw new Error("Choose at most five images or PDF attachments.");
  const body = { jsonrpc: "2.0", id: crypto.randomUUID(), method: "SendMessage", params: { tenant: agent, message: { messageId: crypto.randomUUID(), role: "ROLE_USER", ...(task ? { taskId: task.id, contextId: task.contextID } : {}), parts: [...(prompt ? [{ text: prompt }] : []), ...attachments.map((item) => ({ raw: item.data_base64, mediaType: item.media_type, filename: item.filename }))] }, configuration: { acceptedOutputModes: ["text/plain"] } } };
  if (new TextEncoder().encode(JSON.stringify(body)).length > 24 * 1024 * 1024) throw new Error("Request exceeds the 24 MiB Playground limit.");
  return { path: `/a2a/${encodeURIComponent(agent)}`, body, headers: { "A2A-Version": "1.0" } };
}
export function agentTaskRequest(agent: string, task: AgentTask, method: "GetTask" | "CancelTask") {
  if (!safeID.test(agent) || !safeID.test(task.id)) throw new Error("Invalid agent task ID.");
  return { path: `/a2a/${encodeURIComponent(agent)}`, body: { jsonrpc: "2.0", id: crypto.randomUUID(), method, params: { tenant: agent, id: task.id } }, headers: { "A2A-Version": "1.0" } };
}
export async function runAgentRequest(connection: PlaygroundConnection, request: ReturnType<typeof agentRequest> | ReturnType<typeof agentTaskRequest>, signal: AbortSignal): Promise<AgentRun> {
  if (signal.aborted) throw new DOMException("Request cancelled", "AbortError");
  const start = performance.now();
  const payload = await connection.client.request<Record<string, unknown>>(connection.path(request.path), { method: "POST", body: request.body, headers: request.headers, signal, maximumResponseBytes: 4 * 1024 * 1024 });
  if (signal.aborted) throw new DOMException("Request cancelled", "AbortError");
  if (!object(payload) || payload.jsonrpc !== "2.0" || payload.id !== request.body.id) throw new Error("Gateway returned an invalid agent response.");
  const error = object(payload.error); if (error) throw new Error(typeof error.message === "string" ? error.message : "Agent request failed.");
  const result = object(payload.result), task = object(result?.task) || (request.body.method !== "SendMessage" && object(result?.status) ? result : undefined), message = object(result?.message);
  if (task) {
    const state = object(task.status)?.state;
    if (typeof task.id !== "string" || !safeID.test(task.id) || typeof task.contextId !== "string" || !safeID.test(task.contextId) || typeof state !== "string" || !["TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED"].includes(state)) throw new Error("Gateway returned an invalid agent task.");
    if ("id" in request.body.params && request.body.params.id !== task.id) throw new Error("Gateway returned a different agent task.");
    const artifacts = Array.isArray(task.artifacts) ? task.artifacts : [];
    const latest = object(artifacts.at(-1));
    return { text: contentText(latest?.parts) || contentText(object(object(task.status)?.message)?.parts), task: { id: task.id, contextID: task.contextId, state }, latencyMS: performance.now() - start, response: payload };
  }
  if (!message || !Array.isArray(message.parts)) throw new Error("Gateway returned no agent message or task.");
  return { text: contentText(message.parts), latencyMS: performance.now() - start, response: payload };
}
export type AgentBatchResult = { index: number; prompt: string; status: "completed" | "failed" | "cancelled"; text?: string; error?: string; task?: AgentTask; latencyMS: number };
export function agentBatchPrompts(value: string) {
  const prompts = value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
  if (!prompts.length || prompts.length > 20 || prompts.some((item) => new TextEncoder().encode(item).length > 65536)) throw new Error("Enter 1–20 prompts, one per line, up to 64 KiB each.");
  return prompts;
}
export async function runAgentBatch(connection: PlaygroundConnection, agent: string, prompts: string[], signal: AbortSignal, onResult: (result: AgentBatchResult) => void) {
  if (!prompts.length || prompts.length > 20 || prompts.some((prompt) => !prompt.trim() || new TextEncoder().encode(prompt).length > 65536)) throw new Error("Choose 1–20 prompts up to 64 KiB each.");
  if (!safeID.test(agent)) throw new Error("Select a valid agent ID.");
  let cursor = 0;
  async function worker() {
    while (cursor < prompts.length) {
      const index = cursor++, prompt = prompts[index], start = performance.now();
      if (signal.aborted) { onResult({ index, prompt, status: "cancelled", latencyMS: 0 }); continue; }
      try {
        const result = await runAgentRequest(connection, agentRequest(agent, prompt), signal);
        const completed = !result.task || result.task.state === "TASK_STATE_COMPLETED";
        onResult({ index, prompt, status: completed ? "completed" : "failed", text: result.text, task: result.task, error: completed ? undefined : `Task ended in ${result.task?.state}.`, latencyMS: result.latencyMS });
      } catch (cause) { onResult({ index, prompt, status: signal.aborted ? "cancelled" : "failed", error: signal.aborted ? undefined : cause instanceof Error ? cause.message : "Agent test failed.", latencyMS: performance.now() - start }); }
    }
  }
  await Promise.all(Array.from({ length: Math.min(2, prompts.length) }, () => worker()));
}
export function agentBatchCSV(results: AgentBatchResult[]) {
  return [["index", "prompt", "status", "output", "error", "task_id", "task_state", "latency_ms"], ...[...results].sort((a, b) => a.index - b.index).map((item) => [item.index + 1, item.prompt, item.status, item.text, item.error, item.task?.id, item.task?.state, item.latencyMS])].map((row) => row.map(csvCell).join(",")).join("\r\n") + "\r\n";
}
