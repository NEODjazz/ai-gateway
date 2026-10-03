import type { Message, PlaygroundConnection, TextEndpoint } from "./requests";
import { jsonObject } from "./requests";
import { validFunctionName, type MCPTool } from "./resources";

export type ToolInvocation = {
  id: string; name: string; rawArguments: string; serverID?: string; arguments?: Record<string, unknown>;
  idempotencyKey: string; issue?: string; output?: string; status: "pending" | "failed" | "completed" | "declined"; error?: string;
};
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

export function toolInvocations(endpoint: TextEndpoint, payload: Record<string, unknown>, selected: MCPTool[]): ToolInvocation[] {
  const message = Array.isArray(payload.choices) && object(payload.choices[0]) ? payload.choices[0].message : undefined;
  const values = endpoint === "chat" && object(message) ? message.tool_calls : endpoint === "responses" && Array.isArray(payload.output) ? payload.output.filter((item) => object(item) && item.type === "function_call") : undefined;
  if (values === undefined) return [];
  if (!Array.isArray(values) || values.length > 32) throw new Error("Invalid tool calls: at most 32 function calls can be reviewed per response.");
  const ids = new Set<string>();
  return values.map((value): ToolInvocation => {
    if (!object(value)) throw new Error("Invalid tool call.");
    const fn = endpoint === "chat" ? value.function : value;
    const id = endpoint === "chat" ? value.id : value.call_id;
    if (typeof id !== "string" || !/^[A-Za-z0-9_-]{1,256}$/.test(id) || ids.has(id) || !object(fn) || typeof fn.name !== "string" || !validFunctionName(fn.name) || typeof fn.arguments !== "string" || (endpoint === "chat" && value.type !== "function")) throw new Error("Invalid or duplicate tool call identity. Clear the conversation before retrying.");
    ids.add(id);
    const matches = selected.filter((tool) => tool.name === fn.name);
    let args: Record<string, unknown> | undefined, issue: string | undefined;
    try { args = jsonObject(fn.arguments, "Tool arguments"); } catch (cause) { issue = cause instanceof Error ? cause.message : "Invalid tool arguments."; }
    if (matches.length !== 1) issue = "This function is not bound to one selected MCP tool. It can only be declined.";
    return { id, name: fn.name, rawArguments: fn.arguments, serverID: matches.length === 1 ? matches[0].serverID : undefined, arguments: args, issue,
      idempotencyKey: `playground-tool-${typeof crypto.randomUUID === "function" ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`, status: "pending" };
  });
}

export async function executeTool(connection: PlaygroundConnection, call: ToolInvocation, signal: AbortSignal): Promise<string> {
  if (call.issue || !call.serverID || !call.arguments || call.output !== undefined) throw new Error("This call cannot be executed.");
  if (signal.aborted) throw new DOMException("Tool call cancelled", "AbortError");
  const result = await connection.client.request<unknown>(connection.path(`/v1/mcp/servers/${encodeURIComponent(call.serverID)}/tools/${encodeURIComponent(call.name)}`), {
    method: "POST", body: { arguments: call.arguments }, signal, headers: { "Idempotency-Key": call.idempotencyKey }, maximumResponseBytes: 1024 * 1024
  });
  if (signal.aborted) throw new DOMException("Tool call cancelled", "AbortError");
  if (!object(result) || !Array.isArray(result.content) || result.content.some((item) => !object(item) || typeof item.type !== "string") || (result.isError !== undefined && typeof result.isError !== "boolean")) throw new Error("MCP returned an invalid result. Execution may already have completed; retry uses the same idempotency key.");
  const output = JSON.stringify(result);
  if (new TextEncoder().encode(output).length > 128 * 1024) throw new Error("Tool result exceeds the 128 KiB continuation limit. Execution may already have completed; retry uses the same idempotency key.");
  return output;
}

export function toolOutputs(calls: ToolInvocation[]): Message[] {
  if (!calls.length || calls.some((call) => call.output === undefined)) throw new Error("Resolve every tool call before continuing.");
  return calls.map((call) => ({ role: "tool", tool_call_id: call.id, content: call.output }));
}
