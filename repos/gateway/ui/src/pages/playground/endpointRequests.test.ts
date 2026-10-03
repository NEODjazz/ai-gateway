import { buildEndpointRequest, defaultEndpointSettings, readAttachment, safeMediaURL } from "./endpointRequests";
import { requestCode } from "./requests";

describe("Endpoint request dialects", () => {
  it("constructs native Messages with mandatory reserves and typed history", () => {
    const history = [{ role: "assistant", content: [{ type: "tool_use", id: "call", name: "lookup", input: {} }] }];
    expect(buildEndpointRequest("messages", "model", "hello", { ...defaultEndpointSettings, instructions: "Concise", temperature: "0", topP: "0" }, [], undefined, history).body).toEqual({ model: "model", stream: true, max_tokens: 256, temperature: 0, top_p: 0, system: "Concise", messages: [...history, { role: "user", content: "hello" }] });
    expect(() => buildEndpointRequest("messages", "model", "hello", { ...defaultEndpointSettings, limit: "" })).toThrow("requires");
  });
  it("uses native interaction generation_config and continuation identifiers", () => {
    expect(buildEndpointRequest("interactions", "model", "follow up", { ...defaultEndpointSettings, instructions: "Concise", temperature: "0" }, [], undefined, [], "previous").body).toEqual({ model: "model", input: "follow up", previous_interaction_id: "previous", system_instruction: "Concise", stream: true, generation_config: { max_output_tokens: 256, temperature: 0 } });
  });
  it("omits filenames from image payloads while preserving mask and image content", () => {
    const file = { filename: "input.png", media_type: "image/png", data_base64: "AA==" };
    expect(buildEndpointRequest("image-edits", "image-model", "Edit", defaultEndpointSettings, [file], file).body).toEqual({ model: "image-model", prompt: "Edit", n: 1, response_format: "b64_json", images: [{ media_type: "image/png", data_base64: "AA==" }], mask: { media_type: "image/png", data_base64: "AA==" } });
  });
  it("builds image generation, embedding, speech and transcription dialects", () => {
    expect(buildEndpointRequest("images", "model", "Generate", { ...defaultEndpointSettings, count: "2", size: "1024x1024" }).body).toMatchObject({ prompt: "Generate", n: 2, size: "1024x1024" });
    expect(buildEndpointRequest("embeddings", "model", "Embed", { ...defaultEndpointSettings, dimensions: "128" }).body).toEqual({ model: "model", input: "Embed", encoding_format: "float", dimensions: 128 });
    expect(buildEndpointRequest("speech", "model", "Speak", defaultEndpointSettings).body).toEqual({ model: "model", input: "Speak", voice: "alloy", response_format: "mp3", speed: 1 });
    const file = { filename: "audio.wav", media_type: "audio/wav", data_base64: "AA==" };
    expect(buildEndpointRequest("transcription", "model", "Context", { ...defaultEndpointSettings, language: "ru" }, [file]).body).toEqual({ model: "model", prompt: "Context", language: "ru", response_format: "verbose_json", file });
  });
  it("uses the supported A2A RPC dialect and requires MCP idempotency", () => {
    const agent = buildEndpointRequest("a2a", "", "Question", { ...defaultEndpointSettings, agent: "research" });
    expect(agent.path).toBe("/a2a/research"); expect(agent.headers).toEqual({ "A2A-Version": "1.0" }); expect(agent.body).toMatchObject({ jsonrpc: "2.0", method: "SendMessage", params: { tenant: "research", message: { role: "ROLE_USER", parts: [{ text: "Question" }] } } });
    const first = buildEndpointRequest("mcp", "", "", { ...defaultEndpointSettings, server: "server", tool: "lookup", arguments: '{"query":"test"}' });
    const second = buildEndpointRequest("mcp", "", "", { ...defaultEndpointSettings, server: "server", tool: "lookup" });
    expect(first).toMatchObject({ path: "/v1/mcp/servers/server/tools/lookup", body: { arguments: { query: "test" } } });
    expect(first.headers?.["Idempotency-Key"]).not.toBe(second.headers?.["Idempotency-Key"]);
  });
  it("uses A2A completed task IDs and inline file parts while rejecting raw RPC overrides", () => {
    const request = buildEndpointRequest("a2a", "", "Next", { ...defaultEndpointSettings, agent: "writer" }, [{ filename: "brief.pdf", media_type: "application/pdf", data_base64: "AA==" }], undefined, [], "", [], { id: "task", contextID: "context", state: "TASK_STATE_COMPLETED" });
    expect(request.body).toMatchObject({ params: { message: { taskId: "task", contextId: "context", parts: [{ text: "Next" }, { raw: "AA==", mediaType: "application/pdf", filename: "brief.pdf" }] } } });
    expect(() => buildEndpointRequest("a2a", "", "Next", { ...defaultEndpointSettings, agent: "writer", advanced: '{"extra":true}' })).toThrow("agent and task controls");
    expect(() => buildEndpointRequest("a2a", "", "Next", { ...defaultEndpointSettings, agent: "writer" }, [], undefined, [], "", [], { id: "task", contextID: "context", state: "TASK_STATE_WORKING" })).toThrow("resolve");
  });
  it.each([
    ["images", { count: "11" }, "Image count"], ["embeddings", { dimensions: "0" }, "Dimensions"], ["speech", { speed: "0" }, "Speech speed"],
    ["a2a", { agent: "../escape" }, "valid agent"], ["mcp", { server: "server", tool: "lookup", arguments: "[]" }, "JSON object"],
    ["messages", { advanced: '{"model":"override"}' }, "dedicated control"], ["image-edits", {}, "Attach an image"], ["transcription", {}, "Attach one audio"]
  ] as const)("rejects invalid %s settings", (endpoint, patch, error) => expect(() => buildEndpointRequest(endpoint, "model", "Input", { ...defaultEndpointSettings, ...patch })).toThrow(error));
  it.each(["curl", "python", "javascript"] as const)("includes MCP idempotency in %s code without credentials", (language) => {
    expect(requestCode(language, "/v1/mcp/servers/test/tools/read", { arguments: {} }, "", { "Idempotency-Key": "sample-id" })).toContain("Idempotency-Key");
    expect(() => requestCode(language, "/v1/models", {}, "", { Authorization: "secret" })).toThrow("Unsupported");
  });
  it("exports binary responses as bytes in every code dialect", () => {
    expect(requestCode("curl", "/v1/audio/speech", {}, "", {}, true)).toContain("--output ai-gateway-output.bin");
    expect(requestCode("python", "/v1/audio/speech", {}, "", {}, true)).toContain('with open("ai-gateway-output.bin", "wb")');
    expect(requestCode("javascript", "/v1/audio/speech", {}, "", {}, true)).toContain("Buffer.from(await response.arrayBuffer())");
  });
});

describe("Endpoint attachments and output safety", () => {
  it("reads a bounded file without persisting it", async () => {
    expect(await readAttachment(new File(["test"], "image.png", { type: "image/png" }), "image")).toEqual({ filename: "image.png", media_type: "image/png", data_base64: "dGVzdA==" });
  });
  it("rejects SVG, empty and oversized attachments before reading", async () => {
    await expect(readAttachment(new File(["svg"], "image.svg", { type: "image/svg+xml" }), "image")).rejects.toThrow("Unsupported");
    await expect(readAttachment(new File([], "image.png", { type: "image/png" }), "image")).rejects.toThrow("1 byte");
    await expect(readAttachment(new File(["x".repeat(8 * 1024 * 1024 + 1)], "image.png", { type: "image/png" }), "image")).rejects.toThrow("8 MiB");
  });
  it.each(["javascript:alert(1)", "data:image/svg+xml;base64,AA==", "https://user:pass@example.test/image", "file:///secret"])("rejects unsafe media URL %s", (url) => expect(safeMediaURL(url)).toBeUndefined());
  it("accepts provider HTTPS URLs and allowlisted image data", () => {
    expect(safeMediaURL("https://example.test/image.png")).toBe("https://example.test/image.png"); expect(safeMediaURL("data:image/png;base64,AA==")).toBe("data:image/png;base64,AA==");
  });
});
