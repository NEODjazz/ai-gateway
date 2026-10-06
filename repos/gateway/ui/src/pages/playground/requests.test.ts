import { APIClient } from "../../api/client";
import { buildTextRequest, defaultGenerationSettings, gatewayPath, jsonObject, normalizedBaseURL, playgroundConnection, requestCode, type GenerationSettings } from "./requests";

const request = (endpoint: "chat" | "responses", patch: Partial<GenerationSettings> = {}, options = {}) => buildTextRequest({ endpoint, model: "model-a", input: "hello", instructions: "Be concise", streaming: true, settings: { ...defaultGenerationSettings, ...patch }, ...options });

describe("Playground requests", () => {
  it("uses the correct output reserve and requests streaming Chat usage", () => {
    expect(request("chat")).toMatchObject({ max_completion_tokens: 256, stream_options: { include_usage: true }, messages: [{ role: "system", content: "Be concise" }, { role: "user", content: "hello" }] });
    expect(request("responses")).toMatchObject({ max_output_tokens: 256, input: "hello", instructions: "Be concise" });
    expect(request("responses")).not.toHaveProperty("max_completion_tokens");
  });
  it("requires non-streaming background Responses without silently changing the wire request", () => {
    expect(() => request("responses", { advanced: '{"background":true}' })).toThrow("Disable Stream response");
    expect(request("responses", { advanced: '{"background":true}' }, { streaming: false })).toMatchObject({ background: true, stream: false });
  });
  it("leaves optional provider defaults out and preserves zero sampling values", () => {
    const omitted = request("chat", { maxTokens: " ", temperature: "", topP: "" });
    expect(omitted).not.toHaveProperty("max_completion_tokens"); expect(omitted).not.toHaveProperty("temperature");
    expect(request("responses", { temperature: "0", topP: "0" })).toMatchObject({ temperature: 0, top_p: 0 });
  });
  it("rejects oversized prompt and instructions before provider transport", () => {
    expect(() => request("chat", {}, { input: "x".repeat(1024 * 1024 + 1) })).toThrow("1 MiB");
    expect(() => request("responses", {}, { instructions: "x".repeat(64 * 1024 + 1) })).toThrow("64 KiB");
  });
  it.each([
    { maxTokens: "NaN" }, { maxTokens: "1.5" }, { maxTokens: "0" }, { maxTokens: "9007199254740992" },
    { temperature: "Infinity" }, { temperature: "-1" }, { temperature: "2.01" }, { topP: "-0.1" }, { topP: "1.01" }
  ])("rejects invalid generation parameters %j before transport", (patch) => {
    expect(() => request("chat", patch)).toThrow(/must be/);
  });
  it("uses browser history when API continuity is disabled and only the new input when enabled", () => {
    const history = [{ role: "user", content: "earlier" }, { role: "assistant", content: "answer" }];
    expect(request("responses", {}, { history }).input).toEqual([...history, { role: "user", content: "hello" }]);
    expect(request("responses", {}, { history, previousResponseID: "resp-1" })).toMatchObject({ input: "hello", previous_response_id: "resp-1" });
  });
  it.each(["chat", "responses"] as const)("preserves typed tool history and image input for %s", (endpoint) => {
    const input = [{ type: "image_url", image_url: { url: "data:image/png;base64,AA==" } }];
    const history = [{ role: "assistant", content: null, tool_calls: [{ id: "call-1", function: { name: "lookup", arguments: "{}" } }] }, { role: "tool", tool_call_id: "call-1", content: "result" }];
    const body = request(endpoint, {}, { input, history });
    const messages = endpoint === "chat" ? body.messages : body.input;
    expect(messages).toEqual(endpoint === "chat" ? [{ role: "system", content: "Be concise" }, ...history, { role: "user", content: input }] : [
      { type: "function_call", call_id: "call-1", name: "lookup", arguments: "{}" },
      { type: "function_call_output", call_id: "call-1", output: "result" },
      { role: "user", content: [{ type: "input_image", image_url: "data:image/png;base64,AA==" }] }
    ]);
  });
  it.each(["chat", "responses"] as const)("continues %s using tool results without an extra user turn", (endpoint) => {
    const history = [{ role: "user", content: "Look up" }, { role: "assistant", content: null, tool_calls: [{ id: "call", function: { name: "lookup", arguments: "{}" } }] }];
    const toolOutputs = [{ role: "tool", tool_call_id: "call", content: "Found" }];
    const body = request(endpoint, {}, { input: undefined, history, toolOutputs });
    expect(endpoint === "chat" ? body.messages : body.input).toEqual(endpoint === "chat" ? [{ role: "system", content: "Be concise" }, ...history, ...toolOutputs] : [{ role: "user", content: "Look up" }, { type: "function_call", call_id: "call", name: "lookup", arguments: "{}" }, { type: "function_call_output", call_id: "call", output: "Found" }]);
    if (endpoint === "responses") expect(request(endpoint, {}, { input: undefined, history, toolOutputs, previousResponseID: "response" }).input).toEqual([{ type: "function_call_output", call_id: "call", output: "Found" }]);
  });
  it("generates the two different structured output dialects", () => {
    const schema = { type: "object", properties: { ok: { type: "boolean" } }, required: ["ok"], additionalProperties: false };
    const patch = { responseFormat: "json_schema" as const, schema: JSON.stringify(schema) };
    expect(request("chat", patch).response_format).toEqual({ type: "json_schema", json_schema: { name: "playground_output", schema, strict: true } });
    expect(request("responses", patch).text).toEqual({ format: { type: "json_schema", name: "playground_output", schema, strict: true } });
  });
  it("wraps Responses image input as message content even with API continuity", () => {
    const input = [{ type: "image_url", image_url: { url: "https://example.test/image.png", detail: "low" } }];
    expect(request("responses", {}, { input, previousResponseID: "previous" })).toMatchObject({ previous_response_id: "previous", input: [{ role: "user", content: [{ type: "input_image", image_url: "https://example.test/image.png", detail: "low" }] }] });
  });
  it("preserves native Responses output items in browser continuation", () => {
    const responseItems = [{ type: "reasoning", id: "reasoning", encrypted_content: "opaque" }, { type: "function_call", call_id: "call", name: "lookup", arguments: "{}" }];
    expect(request("responses", {}, { history: [{ role: "assistant", content: "", responseItems }, { role: "tool", tool_call_id: "call", content: "result" }] }).input).toEqual([...responseItems, { type: "function_call_output", call_id: "call", output: "result" }, { role: "user", content: "hello" }]);
  });
  it("sends advanced controls without dropping false, zero or tool constraints", () => {
    const extras = { logprobs: false, frequency_penalty: 0, tools: [{ type: "function", function: { name: "count", parameters: { type: "object", properties: { n: { type: "integer", minimum: 1 } } } } }] };
    expect(request("chat", { advanced: JSON.stringify(extras) })).toMatchObject(extras);
  });
  it.each(["model", "provider", "messages", "stream", "stream_options", "max_tokens", "previous_response_id", "response_format", "__proto__"])("cannot override protected %s from advanced JSON", (name) => {
    expect(() => request("chat", { advanced: `{"${name}":false}` })).toThrow(/cannot override/);
  });
  it.each(["null", "[]", "1", "\"text\"", "{broken"])("rejects invalid JSON object %s", (value) => {
    expect(() => jsonObject(value, "Parameters")).toThrow(/must be/);
  });
  it("bounds JSON by encoded bytes", () => {
    expect(() => jsonObject(JSON.stringify({ text: "я".repeat(32768) }), "Parameters")).toThrow("exceeds 64 KiB");
  });
});

describe("Playground connection", () => {
  it.each(["https://gateway.example.test", "https://gateway.example.test/", "https://gateway.example.test/v1/"])("handles the SDK base URL %s without duplicating v1", (url) => {
    expect(gatewayPath(normalizedBaseURL(url), "/v1/chat/completions")).toBe("https://gateway.example.test/v1/chat/completions");
  });
  it.each(["javascript:alert(1)", "https://user:password@example.test", "https://example.test?key=secret", "https://example.test/#secret", "/relative"])("rejects an invalid base URL %s", (url) => {
    expect(() => normalizedBaseURL(url)).toThrow(/Gateway base URL/);
  });
  it("never forwards the console credential to a custom host", () => {
    expect(() => playgroundConnection(new APIClient(() => "console-token"), "session", "", "https://other.example.test")).toThrow(/never forwarded/);
  });
  it("refuses an untrusted foreign host before any fetch or authorization header", () => {
    const fetchMock = vi.spyOn(globalThis, "fetch");
    expect(() => playgroundConnection(new APIClient(() => "console-token"), "custom", "test-key", "https://other.example.test/v1")).toThrow("not trusted");
    expect(fetchMock).not.toHaveBeenCalled();
  });
  it("uses only the explicit test key and omits browser cookies for independent calls", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("{}"));
    const meta = document.createElement("meta"); meta.name = "ai-gateway-playground-origins"; meta.content = '["https://other.example.test"]'; document.head.append(meta);
    const connection = playgroundConnection(new APIClient(() => "console-token"), "custom", "Bearer test-key", "https://other.example.test/v1");
    meta.remove();
    await connection.client.request(connection.path("/v1/models"));
    expect(fetchMock.mock.calls[0][0]).toBe("https://other.example.test/v1/models");
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).get("Authorization")).toBe("Bearer test-key");
    expect(fetchMock.mock.calls[0][1]?.credentials).toBe("omit");
  });
  it("requires an explicit key and preserves the original session client for the local gateway", () => {
    const client = new APIClient(() => "session-token");
    expect(() => playgroundConnection(client, "custom", "", "")).toThrow("Enter a test API key");
    expect(playgroundConnection(client, "session", "", "").client).toBe(client);
  });
  it("exports runnable code with an environment variable for authorization", () => {
    const body = request("chat", {}, { input: "It's a $value with `backticks`\nnext line" });
    for (const language of ["curl", "python", "javascript"] as const) {
      const code = requestCode(language, "/v1/chat/completions", body, "https://gateway.example.test/v1");
      expect(code).toContain("GATEWAY_API_KEY"); expect(code).toContain("https://gateway.example.test/v1/chat/completions"); expect(code).not.toContain("/v1/v1");
    }
    expect(requestCode("curl", "/v1/chat/completions", body)).toContain(`It'"'"'s`);
    expect(requestCode("curl", "/v1/chat/completions", body)).not.toContain("\n+");
  });
});

describe("A2A code export headers", () => {
  it.each(["curl", "python", "javascript"] as const)("includes the required version in %s without allowing credential overrides", (language) => {
    expect(requestCode(language, "/a2a/research", {}, "", { "A2A-Version": "1.0" })).toContain("A2A-Version");
    expect(() => requestCode(language, "/a2a/research", {}, "", { "A2A-Version": "other" })).toThrow("Unsupported");
    expect(() => requestCode(language, "/a2a/research", {}, "", { Authorization: "override" })).toThrow("Unsupported");
  });
});

describe("Exact native request exports", () => {
  it("executes exported JavaScript with the exact native JSON body", async () => {
    const payload = '{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"query","input":{"id":9007199254740993,"amount":0.1234567890123456789012345}}]}]}';
    const code = requestCode("javascript", "/v1/messages", JSON.parse(payload), "", {}, false, [], payload);
    let sent: RequestInit | undefined;
    const execute = new (Object.getPrototypeOf(async () => {}).constructor)("fetch", "process", "console", code);
    await execute(async (_path: string, options: RequestInit) => { sent = options; return { ok: true, text: async () => "Synthetic response" }; }, { env: { GATEWAY_API_KEY: "synthetic-export-key" } }, { log: () => {} });
    expect(sent?.body).toBe(payload); expect(new Headers(sent?.headers).get("Authorization")).toBe("Bearer synthetic-export-key");
    expect(() => requestCode("curl", "/v1/messages", {}, "", {}, false, [], "{")).toThrow();
  });
});
