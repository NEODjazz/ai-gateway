import { APIClient } from "../../api/client";
import { buildTextRequest, defaultGenerationSettings, playgroundConnection } from "./requests";
import { decideTool, executeTool, toolInvocations, toolOutputs, validateToolContinuation } from "./toolCalls";
const selected = [{ serverID: "weather", name: "forecast", inputSchema: {} }];
const response = (id = "call_1", args = '{}', name = "forecast") => ({ choices: [{ message: { tool_calls: [{ id, type: "function", function: { name, arguments: args } }] } }] });
const connection = playgroundConnection(new APIClient(() => "console-key"), "custom", "test-key", `${window.location.origin}/v1`);
describe("Playground tool approvals", () => {
  const nativeTools = [{ type: "mcp", server_label: "documents", server_url: "https://mcp.example.test", allowed_tools: ["document.search"], require_approval: "always" }];
  const approval = { type: "mcp_approval_request", id: "approval_1", name: "document.search", server_label: "documents", arguments: '{"id":9007199254740993}' };
  it.each([true, false])("retains the native approval ID and explicit decision %s in both continuity modes", async (approved) => {
    const [call] = toolInvocations("responses", { output: [approval] }, [], nativeTools);
    expect(call.nativeApproval).toMatchObject({ serverLabel: "documents", serverURL: "https://mcp.example.test" });
    expect(call.rawArguments).toBe(approval.arguments); expect(() => toolOutputs([call])).toThrow("Resolve every");
    const fetch = vi.spyOn(globalThis, "fetch");
    await expect(executeTool(connection, call, new AbortController().signal)).rejects.toThrow("cannot be executed");
    const decided = decideTool(call, approved), output = { type: "mcp_approval_response", approval_request_id: approval.id, approve: approved };
    expect(decided.status).toBe(approved ? "approved" : "declined");
    for (const previousResponseID of ["resp_1", ""]) {
      const body = buildTextRequest({ endpoint: "responses", model: "model", history: [{ role: "assistant", content: "", responseItems: [approval] }], toolOutputs: toolOutputs([decided]), previousResponseID, instructions: "", streaming: false, settings: defaultGenerationSettings });
      expect(body.input).toEqual(previousResponseID ? [output] : [approval, output]);
    }
    expect(fetch).not.toHaveBeenCalled();
    expect(decideTool(decided, !approved).approved).toBe(!approved);
  });
  it.each([
    { ...approval, id: "../unsafe" }, { ...approval, name: "" }, { ...approval, name: "x\n" },
    { ...approval, server_label: "" }, { ...approval, server_label: "foreign" }, { ...approval, name: "document.delete" }
  ])("rejects native approval identities or calls outside the submitted connection", (item) => {
    expect(() => toolInvocations("responses", { output: [item] }, [], nativeTools)).toThrow();
  });
  it("rejects duplicate, ambiguous and excessive mixed approval batches", () => {
    expect(() => toolInvocations("responses", { output: [approval, approval] }, [], nativeTools)).toThrow("duplicate");
    expect(() => toolInvocations("responses", { output: [approval] }, [], [...nativeTools, ...nativeTools])).toThrow("submitted request");
    expect(() => toolInvocations("responses", { output: [approval] }, [])).toThrow("submitted request");
    const functions = Array.from({ length: 32 }, (_, n) => ({ type: "function_call", call_id: `call_${n}`, name: "forecast", arguments: "{}" }));
    expect(() => toolInvocations("responses", { output: [...functions, approval] }, selected, nativeTools)).toThrow("32");
  });
  it("requires all native and direct function choices while preserving distinct output types", () => {
    const calls = toolInvocations("responses", { output: [approval, { type: "function_call", call_id: "function_1", name: "forecast", arguments: "{}" }] }, selected, nativeTools);
    expect(() => toolOutputs([decideTool(calls[0], false), calls[1]])).toThrow("Resolve every");
    expect(toolOutputs([decideTool(calls[0], false), decideTool(calls[1], false)])).toEqual([
      expect.objectContaining({ responseItems: [{ type: "mcp_approval_response", approval_request_id: "approval_1", approve: false }] }),
      expect.objectContaining({ tool_call_id: "function_1" })
    ]);
    expect(() => decideTool(calls[1], true)).toThrow("explicit execution");
    expect(() => toolOutputs([{ ...calls[0], output: "untyped result" }])).toThrow("Resolve every MCP");
  });
  it.each(["null", "[]", "broken", JSON.stringify({ text: "x".repeat(65536) })])("allows only declining malformed native arguments", (argumentsJSON) => {
    const [call] = toolInvocations("responses", { output: [{ ...approval, arguments: argumentsJSON }] }, [], nativeTools);
    expect(call.issue).toBeTruthy(); expect(() => decideTool(call, true)).toThrow("only be declined");
    expect(toolOutputs([decideTool(call, false)])[0].responseItems).toEqual([{ type: "mcp_approval_response", approval_request_id: "approval_1", approve: false }]);
    expect(() => toolOutputs([{ ...call, approved: true, output: "wrong" }])).toThrow("Resolve every MCP");
  });
  it("prevents replacing, dropping or changing the reviewed MCP connection before continuation", () => {
    const calls = toolInvocations("responses", { output: [approval] }, [], nativeTools);
    expect(() => validateToolContinuation(calls, nativeTools)).not.toThrow();
    expect(() => validateToolContinuation(calls, [{ type: "function", name: "forecast" }, ...nativeTools])).not.toThrow();
    for (const changed of [undefined, [], [...nativeTools, ...nativeTools], [{ ...nativeTools[0], server_url: "https://other.example.test" }], [{ ...nativeTools[0], require_approval: "never" }]]) {
      expect(() => validateToolContinuation(calls, changed)).toThrow("connection changed");
    }
  });
  it("binds functions only to selected tools, retaining server, arguments and a stable idempotency key", () => {
    const [call] = toolInvocations("chat", response("call_1", '{"city":"Rome"}'), selected);
    expect(call).toMatchObject({ id: "call_1", serverID: "weather", name: "forecast", arguments: { city: "Rome" }, status: "pending" });
    expect(call.idempotencyKey).toMatch(/^playground-tool-/);
    expect(toolInvocations("responses", { output: [{ type: "function_call", call_id: "call_1", name: "forecast", arguments: "{}" }, { type: "file_search_call" }] }, selected)).toHaveLength(1);
    expect(() => toolOutputs([call])).toThrow("Resolve every");
    expect(toolOutputs([{ ...call, status: "declined", output: "Declined" }])).toEqual([{ role: "tool", tool_call_id: "call_1", content: "Declined" }]);
  });
  it.each(["[]", "null", "broken", '{"text":"' + "x".repeat(65536) + '"}'])("refuses execution for invalid or oversized arguments", async (args) => {
    const [call] = toolInvocations("chat", response("call", args), selected);
    expect(call.issue).toBeTruthy();
    const mock = vi.spyOn(globalThis, "fetch");
    await expect(executeTool(connection, call, new AbortController().signal)).rejects.toThrow("cannot be executed");
    expect(mock).not.toHaveBeenCalled();
  });
  it("rejects ambiguous or model-supplied server bindings and duplicate IDs", () => {
    expect(toolInvocations("chat", response("call", '{}', "delete"), selected)[0].serverID).toBeUndefined();
    expect(toolInvocations("chat", response(), [...selected, { ...selected[0], serverID: "other" }])[0].issue).toContain("one selected");
    const payload = response(); payload.choices[0].message.tool_calls.push(payload.choices[0].message.tool_calls[0]);
    expect(() => toolInvocations("chat", payload, selected)).toThrow("duplicate");
    expect(() => toolInvocations("chat", response("../unsafe"), selected)).toThrow("identity");
    expect(() => toolInvocations("responses", { output: Array.from({ length: 33 }, (_, n) => ({ type: "function_call", call_id: `call_${n}`, name: "forecast", arguments: "{}" })) }, selected)).toThrow("32");
  });
  it("revalidates changed raw arguments before execution, retaining the reviewed bound and object shape", async () => {
    const [call] = toolInvocations("chat", response(), selected);
    const mock = vi.spyOn(globalThis, "fetch");
    for (const rawArguments of ["null", "[]", "broken", JSON.stringify({ text: "x".repeat(65536) })]) {
      await expect(executeTool(connection, { ...call, rawArguments }, new AbortController().signal)).rejects.toThrow(/Tool arguments/);
    }
    expect(mock).not.toHaveBeenCalled();
  });
  it("preserves numeric tool results for typed model continuation", async () => {
    const output = '{"content":[{"type":"text","text":"ok"}],"structuredContent":{"id":9007199254740993,"decimal":0.1234567890123456789012345}}';
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(output));
    const [call] = toolInvocations("responses", { output: [{ type: "function_call", call_id: "call_exact", name: "forecast", arguments: "{}" }] }, selected);
    const result = await executeTool(connection, call, new AbortController().signal);
    expect(result).toBe(output); expect(mock).toHaveBeenCalledOnce();
    expect(toolOutputs([{ ...call, output: result, status: "completed" }])[0].content).toBe(output);
  });
  it("preserves the reviewed numeric arguments on the wire instead of rounding through JavaScript", async () => {
    const raw = '{"count":9007199254740993,"limit":0.1234567890123456789012345,"nested":{"id":9223372036854775807}}';
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"content":[]}'));
    const [call] = toolInvocations("chat", response("call_exact", raw), selected);
    await executeTool(connection, call, new AbortController().signal);
    expect(mock.mock.calls[0][1]?.body).toBe(`{"arguments":${raw}}`);
    expect(new Headers(mock.mock.calls[0][1]?.headers).get("Idempotency-Key")).toBe(call.idempotencyKey);
  });
  it("uses the independent credential and reuses idempotency on retries; tool errors remain typed results", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"content":[{"type":"text","text":"Unavailable"}],"isError":true}'));
    const [call] = toolInvocations("chat", response(), selected);
    const output = await executeTool(connection, call, new AbortController().signal);
    mock.mockResolvedValue(new Response(output)); await executeTool(connection, call, new AbortController().signal);
    expect(JSON.parse(output).isError).toBe(true);
    expect(mock.mock.calls[0][0]).toBe(`${window.location.origin}/v1/mcp/servers/weather/tools/forecast`);
    for (const [, options] of mock.mock.calls) {
      const headers = new Headers(options?.headers); expect(headers.get("Authorization")).toBe("Bearer test-key"); expect(headers.get("Idempotency-Key")).toBe(call.idempotencyKey); expect(options?.credentials).toBe("omit");
    }
  });
  it("rejects invalid/oversized results and cancelled late responses without silent truncation", async () => {
    const [call] = toolInvocations("chat", response(), selected), controller = new AbortController();
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"content":"wrong"}'));
    await expect(executeTool(connection, call, controller.signal)).rejects.toThrow("invalid result");
    mock.mockResolvedValue(new Response(JSON.stringify({ content: [{ type: "text", text: "x".repeat(128 * 1024) }] })));
    await expect(executeTool(connection, call, controller.signal)).rejects.toThrow("128 KiB");
    mock.mockImplementation(async () => { controller.abort(); return new Response('{"content":[]}'); });
    await expect(executeTool(connection, call, controller.signal)).rejects.toHaveProperty("name", "AbortError");
  });
});
