import type { Message, PlaygroundConnection, TextEndpoint } from "./requests";
import { jsonObject } from "./requests";
import { validFunctionName, type MCPTool } from "./resources";

export type ToolInvocation = {
  id: string; name: string; rawArguments: string; serverID?: string; arguments?: Record<string, unknown>;
  nativeApproval?: { serverLabel: string; serverURL: string; definition: string }; approved?: boolean;
  customTool?: { definition: string }; manualOutput?: string;
  manualFunction?: { definition: string; endpoint: TextEndpoint };
  idempotencyKey: string; issue?: string; output?: string; status: "pending" | "failed" | "completed" | "declined" | "approved"; error?: string;
};
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

function functionDefinitions(tools: unknown, endpoint: TextEndpoint, name: string): Record<string, unknown>[] {
  return Array.isArray(tools) ? tools.filter((tool): tool is Record<string, unknown> => {
    if (!object(tool) || tool.type !== "function") return false;
    const fn = endpoint === "chat" ? tool.function : tool;
    return object(fn) && fn.name === name;
  }) : [];
}

export function toolInvocations(endpoint: TextEndpoint, payload: Record<string, unknown>, selected: MCPTool[], declaredTools: unknown = []): ToolInvocation[] {
  const message = Array.isArray(payload.choices) && object(payload.choices[0]) ? payload.choices[0].message : undefined;
  const values = endpoint === "chat" && object(message) ? message.tool_calls : endpoint === "responses" && Array.isArray(payload.output) ? payload.output.filter((item) => object(item) && ["function_call", "mcp_approval_request", "custom_tool_call"].includes(String(item.type))) : undefined;
  if (values === undefined) return [];
  if (!Array.isArray(values) || values.length > 32) throw new Error("Invalid tool calls: at most 32 tool calls can be reviewed per response.");
  const ids = new Set<string>();
  return values.map((value): ToolInvocation => {
    if (!object(value)) throw new Error("Invalid tool call.");
    const native = endpoint === "responses" && value.type === "mcp_approval_request";
    const custom = endpoint === "responses" && value.type === "custom_tool_call";
    const fn = endpoint === "chat" ? value.function : value;
    const id = endpoint === "chat" || native ? value.id : value.call_id;
    if (typeof id !== "string" || !/^[A-Za-z0-9_-]{1,256}$/.test(id) || ids.has(id) || !object(fn) || typeof fn.name !== "string" || (native ? !validNativeName(fn.name) : !validFunctionName(fn.name)) || typeof (custom ? fn.input : fn.arguments) !== "string" || (endpoint === "chat" && value.type !== "function")) throw new Error("Invalid or duplicate tool call identity. Clear the conversation before retrying.");
    ids.add(id);
    if (custom) {
      const definitions = Array.isArray(declaredTools) ? declaredTools.filter((tool) => object(tool) && tool.type === "custom" && tool.name === fn.name) : [];
      if (definitions.length !== 1) throw new Error("Custom tool call does not match one tool from the submitted request.");
      const input = fn.input as string;
      const issue = new TextEncoder().encode(input).length > 64 * 1024 ? "Tool input exceeds the 64 KiB review limit. This call can only be declined." : value.status !== undefined && value.status !== "completed" ? "Tool input is incomplete. This call can only be declined." : undefined;
      return { id, name: fn.name, rawArguments: input, customTool: { definition: JSON.stringify(definitions[0]) }, idempotencyKey: "", status: "pending", issue };
    }
    const rawArguments = fn.arguments as string;
    const matches = selected.filter((tool) => tool.name === fn.name);
    let args: Record<string, unknown> | undefined, issue: string | undefined;
    try { args = jsonObject(rawArguments, "Tool arguments"); } catch (cause) { issue = cause instanceof Error ? cause.message : "Invalid tool arguments."; }
    if (native) {
      if (typeof value.server_label !== "string" || !validNativeName(value.server_label)) throw new Error("Invalid MCP approval server label.");
      const definitions = Array.isArray(declaredTools) ? declaredTools.filter((tool) => object(tool) && tool.type === "mcp" && tool.server_label === value.server_label) : [];
      const definition = definitions[0];
      if (definitions.length !== 1 || !object(definition) || typeof definition.server_url !== "string" || (definition.allowed_tools !== undefined && (!Array.isArray(definition.allowed_tools) || !definition.allowed_tools.includes(fn.name)))) throw new Error("MCP approval does not match a connection and tool from the submitted request.");
      return { id, name: fn.name, rawArguments, arguments: args, issue, nativeApproval: { serverLabel: value.server_label, serverURL: definition.server_url, definition: JSON.stringify(definition) }, idempotencyKey: "", status: "pending" };
    }
    const definitions = functionDefinitions(declaredTools, endpoint, fn.name);
    if (!matches.length && definitions.length === 1) {
      if (endpoint === "responses" && value.status !== undefined && value.status !== "completed") issue = "Function arguments are incomplete. This call can only be declined.";
      return { id, name: fn.name, rawArguments, arguments: args, issue, manualFunction: { definition: JSON.stringify(definitions[0]), endpoint }, idempotencyKey: "", status: "pending" };
    }
    if (matches.length !== 1) issue = "This function is not bound to one selected MCP tool. It can only be declined.";
    return { id, name: fn.name, rawArguments, serverID: matches.length === 1 ? matches[0].serverID : undefined, arguments: args, issue,
      idempotencyKey: `playground-tool-${typeof crypto.randomUUID === "function" ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`, status: "pending" };
  });
}

export async function executeTool(connection: PlaygroundConnection, call: ToolInvocation, signal: AbortSignal): Promise<string> {
  if (call.nativeApproval || call.customTool || call.manualFunction || call.issue || !call.serverID || !call.arguments || call.output !== undefined) throw new Error("This call cannot be executed.");
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
    if (call.customTool) {
      if (!["completed", "declined"].includes(call.status)) throw new Error("Resolve every custom tool call before continuing.");
      validateManualResult(call.output!);
      if (call.issue && call.status !== "declined") throw new Error("This custom tool call can only be declined.");
      return { role: "tool", content: call.output, responseItems: [{ type: "custom_tool_call_output", call_id: call.id, output: call.output }] };
    }
    if (call.manualFunction) {
      if (!["completed", "declined"].includes(call.status)) throw new Error("Resolve every function tool call before continuing.");
      validateManualResult(call.output!);
      if (call.issue && call.status !== "declined") throw new Error("This function tool call can only be declined.");
    }
    if (!call.nativeApproval) return { role: "tool", tool_call_id: call.id, content: call.output };
    if (typeof call.approved !== "boolean" || (call.approved && call.issue)) throw new Error("Resolve every MCP approval before continuing.");
    return { role: "tool", content: call.output, responseItems: [{ type: "mcp_approval_response", approval_request_id: call.id, approve: call.approved }] };
  });
}

const validNativeName = (name: string) => name.trim().length > 0 && name.length <= 256 && !/[\u0000-\u001f\u007f]/.test(name);

export function decideTool(call: ToolInvocation, approved: boolean): ToolInvocation {
  if (!call.nativeApproval) {
    if (approved) throw new Error(call.customTool || call.manualFunction ? "Client tools require an explicit manual result." : "Function calls require explicit execution.");
    return { ...call, status: "declined", output: JSON.stringify({ isError: true, error: "User declined tool invocation" }), error: undefined };
  }
  if (approved && call.issue) throw new Error("This MCP call can only be declined.");
  return { ...call, approved, status: approved ? "approved" : "declined", output: JSON.stringify({ type: "mcp_approval_response", approval_request_id: call.id, approve: approved }), error: undefined };
}

function validateManualResult(output: string) {
  if (typeof output !== "string") throw new Error("Tool result must be text.");
  if (new TextEncoder().encode(output).length > 128 * 1024) throw new Error("Tool result exceeds the 128 KiB continuation limit.");
}

export function editManualToolResult(call: ToolInvocation, output: string): ToolInvocation {
  if (!(call.customTool || call.manualFunction) || call.issue) throw new Error("This call cannot accept a manual result.");
  validateManualResult(output);
  return { ...call, manualOutput: output, output: undefined, status: "pending", error: undefined };
}

export function provideManualToolResult(call: ToolInvocation, output: string): ToolInvocation {
  const edited = editManualToolResult(call, output);
  return { ...edited, output, status: "completed" };
}

export function validateToolContinuation(calls: ToolInvocation[], tools: unknown) {
  for (const call of calls) {
    const native = call.nativeApproval;
    if (native) {
      const definitions = Array.isArray(tools) ? tools.filter((tool) => object(tool) && tool.type === "mcp" && tool.server_label === native.serverLabel) : [];
      if (definitions.length !== 1 || JSON.stringify(definitions[0]) !== native.definition) throw new Error("The MCP connection changed after this approval was requested. Restore its original configuration or clear the conversation.");
    }
    if (call.customTool) {
      const definitions = Array.isArray(tools) ? tools.filter((tool) => object(tool) && tool.type === "custom" && tool.name === call.name) : [];
      if (definitions.length !== 1 || JSON.stringify(definitions[0]) !== call.customTool.definition) throw new Error("The custom tool definition changed after this call was requested. Restore its original configuration or clear the conversation.");
    }
    if (call.manualFunction) {
      const definitions = functionDefinitions(tools, call.manualFunction.endpoint, call.name);
      if (definitions.length !== 1 || JSON.stringify(definitions[0]) !== call.manualFunction.definition) throw new Error("The function tool definition changed after this call was requested. Restore its original configuration or clear the conversation.");
    }
  }
}
