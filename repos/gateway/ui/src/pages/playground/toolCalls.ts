import type { Message, PlaygroundConnection, TextEndpoint } from "./requests";
import { jsonObject } from "./requests";
import { validFunctionName, type MCPTool } from "./resources";

export type ToolInvocation = {
  id: string; name: string; rawArguments: string; serverID?: string; arguments?: Record<string, unknown>;
  nativeApproval?: { serverLabel: string; serverURL: string; definition: string }; approved?: boolean;
  idempotencyKey: string; issue?: string; output?: string; status: "pending" | "failed" | "completed" | "declined" | "approved"; error?: string;
};
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

export function toolInvocations(endpoint: TextEndpoint, payload: Record<string, unknown>, selected: MCPTool[], declaredTools: unknown = []): ToolInvocation[] {
  const message = Array.isArray(payload.choices) && object(payload.choices[0]) ? payload.choices[0].message : undefined;
  const values = endpoint === "chat" && object(message) ? message.tool_calls : endpoint === "responses" && Array.isArray(payload.output) ? payload.output.filter((item) => object(item) && ["function_call", "mcp_approval_request"].includes(String(item.type))) : undefined;
  if (values === undefined) return [];
  if (!Array.isArray(values) || values.length > 32) throw new Error("Invalid tool calls: at most 32 tool calls can be reviewed per response.");
  const ids = new Set<string>();
  return values.map((value): ToolInvocation => {
    if (!object(value)) throw new Error("Invalid tool call.");
    const native = endpoint === "responses" && value.type === "mcp_approval_request";
    const fn = endpoint === "chat" ? value.function : value;
    const id = endpoint === "chat" || native ? value.id : value.call_id;
    if (typeof id !== "string" || !/^[A-Za-z0-9_-]{1,256}$/.test(id) || ids.has(id) || !object(fn) || typeof fn.name !== "string" || (native ? !validNativeName(fn.name) : !validFunctionName(fn.name)) || typeof fn.arguments !== "string" || (endpoint === "chat" && value.type !== "function")) throw new Error("Invalid or duplicate tool call identity. Clear the conversation before retrying.");
    ids.add(id);
    const matches = selected.filter((tool) => tool.name === fn.name);
    let args: Record<string, unknown> | undefined, issue: string | undefined;
    try { args = jsonObject(fn.arguments, "Tool arguments"); } catch (cause) { issue = cause instanceof Error ? cause.message : "Invalid tool arguments."; }
    if (native) {
      if (typeof value.server_label !== "string" || !validNativeName(value.server_label)) throw new Error("Invalid MCP approval server label.");
      const definitions = Array.isArray(declaredTools) ? declaredTools.filter((tool) => object(tool) && tool.type === "mcp" && tool.server_label === value.server_label) : [];
      const definition = definitions[0];
      if (definitions.length !== 1 || !object(definition) || typeof definition.server_url !== "string" || (definition.allowed_tools !== undefined && (!Array.isArray(definition.allowed_tools) || !definition.allowed_tools.includes(fn.name)))) throw new Error("MCP approval does not match a connection and tool from the submitted request.");
      return { id, name: fn.name, rawArguments: fn.arguments, arguments: args, issue, nativeApproval: { serverLabel: value.server_label, serverURL: definition.server_url, definition: JSON.stringify(definition) }, idempotencyKey: "", status: "pending" };
    }
    if (matches.length !== 1) issue = "This function is not bound to one selected MCP tool. It can only be declined.";
    return { id, name: fn.name, rawArguments: fn.arguments, serverID: matches.length === 1 ? matches[0].serverID : undefined, arguments: args, issue,
      idempotencyKey: `playground-tool-${typeof crypto.randomUUID === "function" ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`, status: "pending" };
  });
}

export async function executeTool(connection: PlaygroundConnection, call: ToolInvocation, signal: AbortSignal): Promise<string> {
  if (call.nativeApproval || call.issue || !call.serverID || !call.arguments || call.output !== undefined) throw new Error("This call cannot be executed.");
  if (signal.aborted) throw new DOMException("Tool call cancelled", "AbortError");
  // Validate again at execution, but preserve exactly what was reviewed.
  jsonObject(call.rawArguments, "Tool arguments");
  const output = await connection.client.requestJSONText(connection.path(`/v1/mcp/servers/${encodeURIComponent(call.serverID)}/tools/${encodeURIComponent(call.name)}`), `{"arguments":${call.rawArguments}}`, {
    method: "POST", signal, headers: { "Idempotency-Key": call.idempotencyKey }, maximumResponseBytes: 1024 * 1024
  });
  if (signal.aborted) throw new DOMException("Tool call cancelled", "AbortError");
  if (output === undefined) throw new Error("MCP returned an invalid result. Execution may already have completed; retry uses the same idempotency key.");
  const result: unknown = JSON.parse(output);
  if (!object(result) || !Array.isArray(result.content) || result.content.some((item) => !object(item) || typeof item.type !== "string") || (result.isError !== undefined && typeof result.isError !== "boolean")) throw new Error("MCP returned an invalid result. Execution may already have completed; retry uses the same idempotency key.");
  if (new TextEncoder().encode(output).length > 128 * 1024) throw new Error("Tool result exceeds the 128 KiB continuation limit. Execution may already have completed; retry uses the same idempotency key.");
  return output;
}

export function toolOutputs(calls: ToolInvocation[]): Message[] {
  if (!calls.length || calls.some((call) => call.output === undefined)) throw new Error("Resolve every tool call before continuing.");
  return calls.map((call) => {
    if (!call.nativeApproval) return { role: "tool", tool_call_id: call.id, content: call.output };
    if (typeof call.approved !== "boolean" || (call.approved && call.issue)) throw new Error("Resolve every MCP approval before continuing.");
    return { role: "tool", content: call.output, responseItems: [{ type: "mcp_approval_response", approval_request_id: call.id, approve: call.approved }] };
  });
}

const validNativeName = (name: string) => name.trim().length > 0 && name.length <= 256 && !/[\u0000-\u001f\u007f]/.test(name);

export function decideTool(call: ToolInvocation, approved: boolean): ToolInvocation {
  if (!call.nativeApproval) {
    if (approved) throw new Error("Function calls require explicit execution.");
    return { ...call, status: "declined", output: JSON.stringify({ isError: true, error: "User declined tool invocation" }), error: undefined };
  }
  if (approved && call.issue) throw new Error("This MCP call can only be declined.");
  return { ...call, approved, status: approved ? "approved" : "declined", output: JSON.stringify({ type: "mcp_approval_response", approval_request_id: call.id, approve: approved }), error: undefined };
}

export function validateToolContinuation(calls: ToolInvocation[], tools: unknown) {
  for (const call of calls) {
    if (!call.nativeApproval) continue;
    const definitions = Array.isArray(tools) ? tools.filter((tool) => object(tool) && tool.type === "mcp" && tool.server_label === call.nativeApproval!.serverLabel) : [];
    if (definitions.length !== 1 || JSON.stringify(definitions[0]) !== call.nativeApproval.definition) throw new Error("The MCP connection changed after this approval was requested. Restore its original configuration or clear the conversation.");
  }
}
