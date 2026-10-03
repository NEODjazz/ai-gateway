import { APIClient } from "../../api/client";
import { defaultGenerationSettings, buildTextRequest, playgroundConnection, requestCode } from "./requests";
import { checkPolicies, emptyResources, parseResourceCatalog, policyChecks, validateMCPSelection, withResources } from "./resources";

const body = (endpoint: "chat" | "responses") => buildTextRequest({ endpoint, model: "model", input: "Prompt", instructions: "", streaming: false, settings: defaultGenerationSettings });
const connection = () => playgroundConnection(new APIClient(() => "test-key"), "custom", "test-virtual-key", "https://gateway.example.test/v1");
describe("Playground resources", () => {
  it.each(["chat", "responses"] as const)("keeps MCP constraints in the %s function dialect", (endpoint) => {
    const schema = { type: "object", properties: { count: { type: "integer", minimum: 1, maximum: 5 } }, required: ["count"], additionalProperties: false };
    const tools = [{ serverID: "server", name: "lookup", inputSchema: schema }];
    const request = withResources(body(endpoint), endpoint, { ...emptyResources, tools });
    const item = (request.tools as Record<string, unknown>[])[0];
    expect(endpoint === "chat" ? (item.function as Record<string, unknown>).parameters : item.parameters).toEqual(schema);
    expect(request).not.toHaveProperty("server_url");
  });
  it("bounds selected schema memory without stripping any schema fields", () => {
    expect(() => validateMCPSelection([{ serverID: "one", name: "lookup", inputSchema: { description: "x".repeat(1024 * 1024) } }])).toThrow("1 MiB");
  });
  it("uses owned Responses resources and leaves advanced tools unchanged when no UI tools are selected", () => {
    const request = withResources(body("responses"), "responses", { ...emptyResources, vectors: ["vs_owned"], codeInterpreter: true, files: ["file_owned"], tags: ["work"] });
    expect(request.tools).toEqual([{ type: "file_search", vector_store_ids: ["vs_owned"] }, { type: "code_interpreter", container: { type: "auto", file_ids: ["file_owned"] } }]);
    expect(request.metadata).toEqual({ playground_tags: '["work"]' });
    expect(withResources({ tools: [{ type: "web_search" }] }, "responses", emptyResources).tools).toEqual([{ type: "web_search" }]);
  });
  it("rejects ambiguous tools, conflicting controls, unsupported dialects and invalid resources", () => {
    const tool = { serverID: "one", name: "lookup", inputSchema: {} };
    expect(() => withResources({}, "chat", { ...emptyResources, tools: [tool, { ...tool, serverID: "two" }] })).toThrow("more than one server");
    expect(() => withResources({ tools: [] }, "chat", { ...emptyResources, tools: [tool] })).toThrow("either");
    expect(() => withResources({}, "chat", { ...emptyResources, vectors: ["vs_one"] })).toThrow("Responses");
    expect(() => withResources({}, "responses", { ...emptyResources, codeInterpreter: true, container: "cntr_one", files: ["file_one"] })).toThrow("automatic container");
    expect(() => withResources({}, "responses", { ...emptyResources, vectors: ["../foreign"] })).toThrow("invalid");
    expect(() => withResources({}, "chat", { ...emptyResources, tools: Array.from({ length: 33 }, (_, i) => ({ ...tool, name: `tool_${i}` })) })).toThrow("32 MCP");
  });
  it("checks selected policies through the explicit credential transport and stops on invalid or blocked decisions", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response('{"allowed":false}'));
    await expect(checkPolicies(connection(), ["strict", "other"], "Prompt", "model", new AbortController().signal)).rejects.toThrow("blocked by policy strict");
    expect(mock).toHaveBeenCalledOnce();
    expect(mock.mock.calls[0][0]).toBe("https://gateway.example.test/guardrails/apply_guardrail");
    expect(mock.mock.calls[0][1]?.credentials).toBe("omit");
    mock.mockResolvedValueOnce(new Response('{}'));
    await expect(checkPolicies(connection(), ["strict"], "Prompt", "model", new AbortController().signal)).rejects.toThrow("invalid decision");
  });
  it("does not admit generation after cancellation even if preflight transport ignores AbortSignal", async () => {
    const controller = new AbortController();
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => { controller.abort(); return new Response('{"allowed":true}'); });
    await expect(checkPolicies(connection(), ["strict"], "Prompt", "model", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(() => policyChecks(["strict"], "x".repeat(65537), "model")).toThrow("64 KiB");
  });
  it.each(["curl", "python", "javascript"] as const)("exports %s checks that gate inference without credentials", (language) => {
    const code = requestCode(language, "/v1/responses", body("responses"), "https://gateway.example.test", {}, false, policyChecks(["strict"], "It's a prompt", "model"));
    expect(code.indexOf("apply_guardrail")).toBeLessThan(code.lastIndexOf("/v1/responses"));
    expect(code).toContain("allowed"); expect(code).toContain("GATEWAY_API_KEY"); expect(code).not.toContain("test-virtual-key");
    if (language === "curl") expect(code).toContain("set -euo pipefail");
  });
  it("validates nested catalog entries rather than rendering malformed data", () => {
    const catalog = { mcp_servers: [], mcp_toolsets: [], policies: [], tags: [], agents: [], truncated: false };
    expect(parseResourceCatalog(catalog)).toEqual(catalog);
    expect(() => parseResourceCatalog({ ...catalog, mcp_toolsets: [{ id: "one", name: "One", server_ids: [], tool_grants: null }] })).toThrow("toolset grants");
    expect(() => parseResourceCatalog({ ...catalog, policies: ["../bad"] })).toThrow("invalid resource catalog");
    expect(() => parseResourceCatalog({ ...catalog, tags: Array(257).fill("work") })).toThrow("invalid resource catalog");
  });
});
