import type { PlaygroundConnection, TextEndpoint } from "./requests";

export type Resource = { id: string; name: string };
export type ResourceCatalog = {
  mcp_servers: Resource[]; mcp_toolsets: (Resource & { server_ids: string[]; tool_grants: Record<string, string[]> })[];
  policies: string[]; policy_error?: string; tags: string[]; truncated: boolean;
  agents: (Resource & { model: string; execution_supported: boolean })[];
};
export type MCPTool = { serverID: string; name: string; description?: string; inputSchema: Record<string, unknown> };
export type ResourceSelection = { tools: MCPTool[]; tags: string[]; policies: string[]; vectors: string[]; codeInterpreter: boolean; container: string; files: string[] };
export const emptyResources: ResourceSelection = { tools: [], tags: [], policies: [], vectors: [], codeInterpreter: false, container: "auto", files: [] };
export const validResourceID = (id: string) => /^[A-Za-z0-9._:-]{1,128}$/.test(id);
export const validFunctionName = (name: string) => /^[A-Za-z0-9_-]{1,64}$/.test(name);

export function validateMCPSelection(tools: MCPTool[]) {
  if (tools.length > 32) throw new Error("Select at most 32 MCP tools.");
  if (new TextEncoder().encode(JSON.stringify(tools)).length > 1024 * 1024) throw new Error("Selected MCP definitions exceed the 1 MiB browser limit.");
}

export function parseResourceCatalog(value: unknown): ResourceCatalog {
  const record = (item: unknown): item is Record<string, unknown> => !!item && typeof item === "object" && !Array.isArray(item);
  const resources = (items: unknown): items is Resource[] => Array.isArray(items) && items.length <= 256 && items.every((item) => record(item) && typeof item.id === "string" && validResourceID(item.id) && typeof item.name === "string" && item.name.length <= 256);
  const strings = (items: unknown, valid: (item: string) => boolean) => Array.isArray(items) && items.length <= 256 && items.every((item) => typeof item === "string" && valid(item));
  if (!record(value) || !resources(value.mcp_servers) || !resources(value.mcp_toolsets) || !resources(value.agents) ||
    !strings(value.policies, (item) => /^[A-Za-z0-9._-]{1,128}$/.test(item)) || !strings(value.tags, (item) => item.length <= 512) || typeof value.truncated !== "boolean" ||
    (value.policy_error !== undefined && typeof value.policy_error !== "string")) throw new Error("Gateway returned an invalid resource catalog.");
  for (const toolset of value.mcp_toolsets as unknown as Record<string, unknown>[]) {
    if (!strings(toolset.server_ids, validResourceID) || !record(toolset.tool_grants) || Object.keys(toolset.tool_grants).length > 256 || Object.entries(toolset.tool_grants).some(([server, patterns]) => !validResourceID(server) || !strings(patterns, (item) => item.length <= 256))) throw new Error("Gateway returned invalid toolset grants.");
  }
  if ((value.agents as unknown as Record<string, unknown>[]).some((agent) => typeof agent.model !== "string" || agent.model.length > 256 || typeof agent.execution_supported !== "boolean")) throw new Error("Gateway returned invalid agent entries.");
  return value as unknown as ResourceCatalog;
}

export function withResources(body: Record<string, unknown>, endpoint: TextEndpoint, selection: ResourceSelection): Record<string, unknown> {
  const tools: unknown[] = [];
  validateMCPSelection(selection.tools);
  const names = new Set<string>();
  for (const tool of selection.tools) {
    if (!validResourceID(tool.serverID) || !validFunctionName(tool.name) || !tool.inputSchema || typeof tool.inputSchema !== "object" || Array.isArray(tool.inputSchema)) throw new Error("An MCP tool cannot be represented by this function API.");
    if (names.has(tool.name)) throw new Error(`Tool name ${tool.name} is selected from more than one server. Select one definition.`);
    names.add(tool.name);
    const fn = { name: tool.name, ...(tool.description ? { description: tool.description } : {}), parameters: tool.inputSchema };
    tools.push(endpoint === "chat" ? { type: "function", function: fn } : { type: "function", ...fn });
  }
  if (selection.vectors.length > 20 || selection.files.length > 16) throw new Error("Select at most 20 vector stores and 16 code files.");
  if ([...selection.vectors, ...selection.files].some((id) => !validResourceID(id)) || !validResourceID(selection.container)) throw new Error("A selected resource ID is invalid.");
  if (endpoint === "chat" && (selection.vectors.length || selection.codeInterpreter || selection.files.length)) throw new Error("Vector search and code interpreter require the Responses endpoint.");
  if (selection.vectors.length) tools.push({ type: "file_search", vector_store_ids: selection.vectors });
  if (selection.files.length && !selection.codeInterpreter) throw new Error("Enable code interpreter before selecting code files.");
  if (selection.codeInterpreter) {
    if (selection.container !== "auto" && selection.files.length) throw new Error("Use an automatic container for selected files, or attach files to the existing container separately.");
    tools.push({ type: "code_interpreter", container: selection.container === "auto" ? { type: "auto", ...(selection.files.length ? { file_ids: selection.files } : {}) } : selection.container });
  }
  if (tools.length && body.tools !== undefined) throw new Error("Configure tools using either resource selection or advanced JSON.");
  const result: Record<string, unknown> = { ...body, ...(tools.length ? { tools } : {}) };
  if (selection.tags.length) {
    const tags = JSON.stringify(selection.tags);
    if ([...tags].length > 512) throw new Error("Selected tag metadata exceeds 512 characters.");
    if (body.metadata !== undefined && (!body.metadata || typeof body.metadata !== "object" || Array.isArray(body.metadata))) throw new Error("Metadata must be a JSON object.");
    const metadata = body.metadata as Record<string, unknown> | undefined;
    if (metadata?.playground_tags !== undefined) throw new Error("Configure playground_tags using the tag controls.");
    if (Object.keys(metadata || {}).length >= 16) throw new Error("Metadata allows at most 16 entries, including selected tags.");
    result.metadata = { ...metadata, playground_tags: tags };
  }
  if (new TextEncoder().encode(JSON.stringify(result)).length > 24 * 1024 * 1024) throw new Error("Configured request exceeds the 24 MiB Playground limit.");
  return result;
}

export function policyChecks(policies: string[], text: string, model: string) {
  if (policies.length > 4 || new Set(policies).size !== policies.length || policies.some((name) => !/^[A-Za-z0-9._-]{1,128}$/.test(name))) throw new Error("Select up to four distinct guardrail policies.");
  if (policies.length && (!text || new TextEncoder().encode(text).length > 65536)) throw new Error("Additional policy checks require a text prompt of at most 64 KiB.");
  return policies.map((name) => ({ path: "/guardrails/apply_guardrail", body: { guardrail_name: name, text, model } }));
}

export async function checkPolicies(connection: PlaygroundConnection, policies: string[], text: string, model: string, signal: AbortSignal) {
  for (const check of policyChecks(policies, text, model)) {
    if (signal.aborted) throw new DOMException("Request cancelled", "AbortError");
    const response = await connection.client.request<{ allowed: boolean }>(connection.path(check.path), { method: "POST", body: check.body, maximumResponseBytes: 1024 * 1024, signal });
    if (signal.aborted) throw new DOMException("Request cancelled", "AbortError");
    if (response?.allowed !== true) throw new Error(response?.allowed === false ? `Prompt blocked by policy ${check.body.guardrail_name}.` : "Policy returned an invalid decision. Generation was not started.");
  }
}
