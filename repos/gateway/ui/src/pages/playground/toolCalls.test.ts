import { APIClient } from "../../api/client";
import { playgroundConnection } from "./requests";
import { executeTool, toolInvocations, toolOutputs } from "./toolCalls";
const selected = [{ serverID: "weather", name: "forecast", inputSchema: {} }];
const response = (id = "call_1", args = '{}', name = "forecast") => ({ choices: [{ message: { tool_calls: [{ id, type: "function", function: { name, arguments: args } }] } }] });
const connection = playgroundConnection(new APIClient(() => "console-key"), "custom", "test-key", `${window.location.origin}/v1`);
describe("Playground tool approvals", () => {
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
