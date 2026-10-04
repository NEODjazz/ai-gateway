import { APIClient } from "../../api/client";
import { playgroundConnection } from "./requests";
import { runResponseResource, runText } from "./runText";

function sse(events: unknown[]) {
  const encoder = new TextEncoder();
  return new Response(new ReadableStream({ start(controller) {
    for (const event of events) controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`));
    controller.enqueue(encoder.encode("data: [DONE]\n\n")); controller.close();
  } }), { headers: { "Content-Type": "text/event-stream" } });
}
const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const options = () => ({ signal: new AbortController().signal, sessionID: "test-session" });

describe("Playground text execution", () => {
  it.each(["refresh", "cancel"] as const)("uses the authenticated %s resource operation without replaying generation", async (operation) => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"id":"resp_1","status":"completed","output_text":"Final","usage":{"input_tokens":0,"output_tokens":2}}'));
    const result = await runResponseResource(connection(), "resp_1", operation, options().signal, "test-session");
    expect(result).toMatchObject({ id: "resp_1", text: "Final", usage: { input_tokens: 0 }, streamed: false });
    expect(mock.mock.calls[0][0]).toBe(`/v1/responses/resp_1${operation === "cancel" ? "/cancel" : ""}`);
    expect(mock.mock.calls[0][1]?.method).toBe(operation === "cancel" ? "POST" : "GET");
    expect(mock.mock.calls[0][1]?.body).toBeUndefined();
    expect(new Headers(mock.mock.calls[0][1]?.headers).get("Authorization")).toBe("Bearer test-key");
  });
  it("retains failed lifecycle status and rejects mismatches, missing identities and late cancelled reads", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"id":"resp_1","status":"failed","error":{"message":"Upstream failed"}}'));
    expect(await runResponseResource(connection(), "resp_1", "refresh", options().signal, "test-session")).toMatchObject({ response: { status: "failed", error: { message: "Upstream failed" } } });
    for (const payload of [{ id: "other", status: "completed" }, { id: "resp_1", status: "unknown" }]) {
      mock.mockResolvedValueOnce(new Response(JSON.stringify(payload)));
      await expect(runResponseResource(connection(), "resp_1", "refresh", options().signal, "test-session")).rejects.toThrow("mismatching ID or invalid status");
    }
    mock.mockResolvedValueOnce(new Response('{"status":"queued"}'));
    await expect(runText(connection(), "responses", { model: "model" }, options())).rejects.toThrow("valid response ID");
    await expect(runResponseResource(connection(), "../bad", "refresh", options().signal, "test-session")).rejects.toThrow("valid response ID");
    const abort = new AbortController(); mock.mockImplementation(async () => { abort.abort(); return new Response('{}'); });
    await expect(runResponseResource(connection(), "resp_1", "refresh", abort.signal, "test-session")).rejects.toMatchObject({ name: "AbortError" });
  });
  it("collects public text, usage-only events and latency independently of reasoning", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([
      { id: "chat-1", choices: [{ delta: { reasoning_content: "Reasoning" } }] },
      { model: "upstream", choices: [{ delta: { content: "Hello" } }] },
      { choices: [], usage: { prompt_tokens: 0, completion_tokens: 2, total_tokens: 2 } }
    ]));
    let time = 0;
    const result = await runText(connection(), "chat", { model: "public", stream: true }, { ...options(), clock: () => time += 10 });
    expect(result).toMatchObject({ text: "Hello", reasoning: "Reasoning", id: "chat-1", model: "upstream", streamed: true, firstTokenMS: 10, latencyMS: 20, usage: { prompt_tokens: 0, total_tokens: 2 } });
  });
  it("assembles split tool arguments into typed assistant history without converting them to text", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([
      { choices: [{ delta: { tool_calls: [{ index: 0, id: "call-1", function: { name: "lookup", arguments: '{"query":' } }] } }] },
      { choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: '"example"}' } }] }, finish_reason: "tool_calls" }] }
    ]));
    const result = await runText(connection(), "chat", { model: "tools", stream: true }, options());
    expect(result.text).toBe(""); expect(result.firstTokenMS).toBeUndefined();
    expect(result.response.choices).toEqual([{ message: { role: "assistant", content: null, tool_calls: [{ id: "call-1", type: "function", function: { name: "lookup", arguments: '{"query":"example"}' } }] }, finish_reason: "tool_calls" }]);
  });
  it("keeps Responses arguments and reasoning separate and uses the completed response", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([
      { type: "response.function_call_arguments.delta", delta: "arguments" },
      { type: "response.reasoning_summary_text.delta", delta: "Reasoning summary" },
      { type: "response.output_text.delta", delta: "Answer" },
      { type: "response.completed", response: { id: "resp-1", model: "upstream", output_text: "Answer", usage: { input_tokens: 4, output_tokens: 2 } } }
    ]));
    expect(await runText(connection(), "responses", { model: "public", stream: true }, options())).toMatchObject({ text: "Answer", reasoning: "Reasoning summary", id: "resp-1", usage: { input_tokens: 4 } });
  });
  it("retains custom tool input only in finalized Responses output, separate from public text", async () => {
    const call = { type: "custom_tool_call", call_id: "custom_1", name: "query", input: 'status:open\nowner:"demo"' };
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([
      { type: "response.custom_tool_call_input.delta", delta: "status:open" },
      { type: "response.custom_tool_call_input.delta", delta: '\nowner:"demo"' },
      { type: "response.completed", response: { id: "resp_custom", status: "completed", output: [call], usage: { input_tokens: 4, output_tokens: 2 } } }
    ]));
    const onText = vi.fn();
    const result = await runText(connection(), "responses", { model: "model", stream: true }, { ...options(), onText });
    expect(result.text).toBe(""); expect(result.firstTokenMS).toBeUndefined(); expect(onText.mock.calls.every(([text]) => text === "")).toBe(true); expect(result.response.output).toEqual([call]);
  });
  it("does not expose partial custom input from an interrupted Responses stream for execution", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('data: {"type":"response.custom_tool_call_input.delta","delta":"partial code"}\n\n', { headers: { "Content-Type": "text/event-stream" } }));
    await expect(runText(connection(), "responses", { model: "model", stream: true }, options())).rejects.toThrow("before the response completed");
  });
  it("accepts a single JSON fallback without replaying and leaves TTFT unavailable", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ id: "resp-1", output: [{ type: "message", content: [{ type: "output_text", text: "JSON answer" }] }] }), { headers: { "Content-Type": "application/json" } }));
    const result = await runText(connection(), "responses", { model: "model", stream: true }, options());
    expect(result).toMatchObject({ text: "JSON answer", streamed: false });
    expect(result.firstTokenMS).toBeUndefined(); expect(fetchMock).toHaveBeenCalledOnce();
  });
  it("renders refusals in JSON and streamed Chat while preserving the refusal field", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(new Response('{"choices":[{"message":{"role":"assistant","content":null,"refusal":"Cannot assist"}}]}'));
    expect((await runText(connection(), "chat", { model: "model" }, options())).text).toBe("Cannot assist");
    mock.mockResolvedValueOnce(sse([{ choices: [{ delta: { refusal: "Cannot assist" }, finish_reason: "stop" }] }]));
    const streamed = await runText(connection(), "chat", { model: "model", stream: true }, options());
    expect(streamed.text).toBe("Cannot assist");
    expect(streamed.response.choices).toEqual([{ message: { role: "assistant", content: null, refusal: "Cannot assist" }, finish_reason: "stop" }]);
  });
  it("renders Responses refusals and retains JSON reasoning summaries separately", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ output: [{ type: "reasoning", summary: [{ type: "summary_text", text: "Summary" }] }, { type: "message", content: [{ type: "refusal", refusal: "Cannot assist" }] }] })));
    expect(await runText(connection(), "responses", { model: "model" }, options())).toMatchObject({ text: "Cannot assist", reasoning: "Summary" });
  });
  it("does not treat nested Responses failure as a success", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([{ type: "response.failed", response: { status: "failed", error: { message: "Failed upstream" } } }]));
    await expect(runText(connection(), "responses", { model: "model", stream: true }, options())).rejects.toThrow("Failed upstream");
  });
  it("checks cancellation even when JSON transport ignores AbortSignal", async () => {
    const controller = new AbortController();
    const onText = vi.fn();
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => { controller.abort(); return new Response('{"output_text":"Late answer"}'); });
    await expect(runText(connection(), "responses", { model: "model", stream: false }, { signal: controller.signal, sessionID: "session", onText })).rejects.toMatchObject({ name: "AbortError" });
    expect(onText).not.toHaveBeenCalled();
  });
  it.each(["chat", "responses"] as const)("rejects an interrupted %s stream", async (endpoint) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('data: {"type":"response.output_text.delta","delta":"Partial","choices":[{"delta":{"content":"Partial"}}]}\n\n', { headers: { "Content-Type": "text/event-stream" } }));
    await expect(runText(connection(), endpoint, { model: "model", stream: true }, options())).rejects.toThrow("before the response completed");
  });
  it("bounds the number and size of visible diagnostic events", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse(Array.from({ length: 70 }, () => ({ choices: [], debug: "x".repeat(12000) }))));
    const result = await runText(connection(), "chat", { model: "model", stream: true }, options());
    expect(result.eventCount).toBe(70); expect(result.events).toHaveLength(50);
    expect(result.events.every((event) => event.data.length < 8300 && event.data.includes("truncated"))).toBe(true);
  });
  it("rejects oversized tool arguments across different calls", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([0, 1, 2].map((index) => ({ choices: [{ delta: { tool_calls: [{ index, function: { name: "tool", arguments: "x".repeat(1024 * 1024) } }] } }] }))));
    await expect(runText(connection(), "chat", { model: "model", stream: true }, options())).rejects.toThrow("Tool arguments exceed");
  });
  it.each([null, [], 1, "bad"])("rejects a non-object stream payload %j", async (event) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse([event]));
    await expect(runText(connection(), "chat", { model: "model", stream: true }, options())).rejects.toThrow("Malformed streaming event");
  });
});
