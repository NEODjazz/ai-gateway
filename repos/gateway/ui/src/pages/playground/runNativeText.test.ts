import { APIClient } from "../../api/client";
import { playgroundConnection } from "./requests";
import { runNativeText } from "./runNativeText";

const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const options = () => new AbortController().signal;
function stream(events: unknown[]) { return new Response(events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(""), { headers: { "Content-Type": "text/event-stream" } }); }
describe("Native text execution", () => {
  it("assembles Messages tools, reasoning signatures and usage without mixing them into text", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([
      { type: "message_start", message: { id: "message", usage: { input_tokens: 0 } } },
      { type: "content_block_start", index: 0, content_block: { type: "thinking", thinking: "", signature: "" } },
      { type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: "Plan" } },
      { type: "content_block_delta", index: 0, delta: { type: "signature_delta", signature: "signature" } },
      { type: "content_block_start", index: 1, content_block: { type: "text", text: "" } },
      { type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "Answer" } },
      { type: "content_block_start", index: 2, content_block: { type: "tool_use", id: "call", name: "lookup", input: {} } },
      { type: "content_block_delta", index: 2, delta: { type: "input_json_delta", partial_json: '{"query":' } },
      { type: "content_block_delta", index: 2, delta: { type: "input_json_delta", partial_json: '"test"}' } },
      { type: "message_delta", delta: { stop_reason: "tool_use" }, usage: { output_tokens: 5 } }, { type: "message_stop" }
    ]));
    const result = await runNativeText(connection(), "messages", { stream: true }, options(), vi.fn());
    expect(result).toMatchObject({ text: "Answer", reasoning: "Plan", response: { id: "message", usage: { input_tokens: 0, output_tokens: 5 }, content: [{ type: "thinking", thinking: "Plan", signature: "signature" }, { type: "text", text: "Answer" }, { type: "tool_use", id: "call", name: "lookup", input: { query: "test" } }] } });
    expect(result.firstTokenMS).toEqual(expect.any(Number));
  });
  it("handles Interactions thought deltas and completed native usage", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([
      { event_type: "step.delta", delta: { type: "thought_summary", text: "Plan" } },
      { event_type: "step.delta", delta: { type: "text", text: "Answer" } },
      { event_type: "interaction.completed", interaction: { id: "interaction", status: "completed", usage: { total_input_tokens: 0, total_output_tokens: 2 } } }
    ]));
    expect(await runNativeText(connection(), "interactions", { stream: true }, options(), vi.fn())).toMatchObject({ text: "Answer", reasoning: "Plan", response: { id: "interaction", usage: { total_input_tokens: 0 } } });
  });
  it.each(["messages", "interactions"] as const)("does not replay a %s JSON fallback", async (endpoint) => {
    const payload = endpoint === "messages" ? { content: [{ type: "text", text: "Answer" }] } : { steps: [{ type: "model_output", content: [{ type: "text", text: "Answer" }] }] };
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(payload), { headers: { "Content-Type": "application/json" } }));
    expect(await runNativeText(connection(), endpoint, { stream: true }, options(), vi.fn())).toMatchObject({ text: "Answer", firstTokenMS: undefined }); expect(mock).toHaveBeenCalledOnce();
  });
  it("preserves indexed interaction functions when completion contains only metadata", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([
      { event_type: "step.start", index: 0, step: { type: "model_output", id: "step" } },
      { event_type: "step.delta", index: 0, delta: { type: "text", text: "Answer" } },
      { event_type: "step.start", index: 1, step: { type: "function_call", id: "call", name: "lookup" } },
      { event_type: "step.delta", index: 1, delta: { type: "arguments_delta", arguments: '{"count":' } },
      { event_type: "step.delta", index: 1, delta: { type: "arguments_delta", arguments: '1}' } },
      { event_type: "interaction.completed", interaction: { id: "interaction", status: "completed" } }
    ]));
    expect(await runNativeText(connection(), "interactions", { stream: true }, options(), vi.fn())).toMatchObject({ text: "Answer", response: { steps: [{ type: "model_output", content: [{ type: "text", text: "Answer" }] }, { type: "function_call", id: "call", name: "lookup", arguments: { count: 1 } }] } });
  });
  it("uses authoritative final interaction content and keeps initial block first-token timing", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([{ event_type: "step.delta", delta: { text: "Partial" } }, { event_type: "interaction.completed", interaction: { steps: [{ type: "model_output", content: [{ type: "text", text: "Final answer" }] }, { type: "thought", content: [{ type: "text", text: "Final reasoning" }] }] } }]));
    expect(await runNativeText(connection(), "interactions", { stream: true }, options(), vi.fn())).toMatchObject({ text: "Final answer", reasoning: "Final reasoning" });
    mock.mockResolvedValue(stream([{ type: "content_block_start", index: 0, content_block: { type: "text", text: "Initial" } }, { type: "message_stop" }]));
    expect(await runNativeText(connection(), "messages", { stream: true }, options(), vi.fn())).toMatchObject({ firstTokenMS: expect.any(Number), text: "Initial" });
  });
  it("rejects interaction tool deltas without a matching step or valid JSON", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([{ event_type: "step.delta", index: 0, delta: { type: "arguments_delta", arguments: "{}" } }]));
    await expect(runNativeText(connection(), "interactions", { stream: true }, options(), vi.fn())).rejects.toThrow("unavailable function");
    mock.mockResolvedValue(stream([{ event_type: "step.start", index: 0, step: { type: "function_call", id: "call", name: "lookup" } }, { event_type: "step.delta", index: 0, delta: { type: "arguments_delta", arguments: "{" } }, { event_type: "interaction.completed", interaction: { status: "completed" } }]));
    await expect(runNativeText(connection(), "interactions", { stream: true }, options(), vi.fn())).rejects.toThrow("invalid JSON");
  });
  it("rejects an interrupted stream and an invalid block reference", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([{ type: "message_start", message: { id: "partial" } }]));
    await expect(runNativeText(connection(), "messages", { stream: true }, options(), vi.fn())).rejects.toThrow("before completion");
    mock.mockResolvedValue(stream([{ type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Unexpected" } }]));
    await expect(runNativeText(connection(), "messages", { stream: true }, options(), vi.fn())).rejects.toThrow("unavailable content block");
  });
  it("does not accept native nested failure or publish cancelled JSON output", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([{ event_type: "interaction.failed", interaction: { status: "failed", error: { message: "Native failed" } } }]));
    await expect(runNativeText(connection(), "interactions", { stream: true }, options(), vi.fn())).rejects.toThrow("Native failed");
    const controller = new AbortController(), onText = vi.fn();
    mock.mockImplementation(async () => { controller.abort(); return new Response('{"content":[{"type":"text","text":"Late"}]}'); });
    await expect(runNativeText(connection(), "messages", { stream: false }, controller.signal, onText)).rejects.toMatchObject({ name: "AbortError" }); expect(onText).not.toHaveBeenCalled();
  });
  it("bounds raw native events and rejects oversized text", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(stream([...Array.from({ length: 60 }, () => ({ type: "ping", debug: "x".repeat(10000) })), { type: "message_stop" }]));
    const result = await runNativeText(connection(), "messages", { stream: true }, options(), vi.fn());
    expect(result.events).toHaveLength(50); expect(result.events.every((event) => event.data.length <= 8192)).toBe(true);
    mock.mockResolvedValue(new Response(JSON.stringify({ content: [{ type: "text", text: "x".repeat(2 * 1024 * 1024 + 1) }] })));
    await expect(runNativeText(connection(), "messages", { stream: false }, options(), vi.fn())).rejects.toThrow("2 MiB");
  });
});
