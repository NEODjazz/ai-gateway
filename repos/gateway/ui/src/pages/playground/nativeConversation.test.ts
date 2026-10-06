import { nativeHistory, nativeToolCalls, nativeToolContent, nativeUserContent, validateNativeContinuation, type NativeTurn } from "./nativeConversation";
import { retainConversation } from "./attachments";

describe("Native conversation contracts", () => {
  it("uses distinct image/PDF input dialects without changing attachment contents", () => {
    const files = [{ filename: "image.png", media_type: "image/png", data_base64: "AA==" }, { filename: "doc.pdf", media_type: "application/pdf", data_base64: "AQ==" }];
    expect(nativeUserContent("messages", "Inspect", files)).toEqual([{ type: "text", text: "Inspect" }, { type: "image", source: { type: "base64", media_type: "image/png", data: "AA==" } }, { type: "document", source: { type: "base64", media_type: "application/pdf", data: "AQ==" } }]);
    expect(nativeUserContent("interactions", "Inspect", files)).toEqual([{ type: "input_text", text: "Inspect" }, { type: "input_image", image_url: "data:image/png;base64,AA==" }, { type: "input_file", filename: "doc.pdf", file_data: "data:application/pdf;base64,AQ==" }]);
  });
  it("rejects malformed, duplicate and oversized native tool calls", () => {
    const call = { type: "tool_use", id: "call", name: "lookup", input: {} };
    expect(nativeToolCalls("messages", { content: [call] })).toMatchObject([{ id: "call", name: "lookup", arguments: {}, rawArguments: "{}", issue: expect.stringContaining("only be declined") }]);
    for (const content of [[call, call], [{ ...call, input: [] }], [{ ...call, id: "../escape" }], [{ ...call, input: { text: "x".repeat(65536) } }], Array.from({ length: 33 }, (_, i) => ({ ...call, id: `call-${i}` }))]) expect(() => nativeToolCalls("messages", { content })).toThrow("tool call");
    expect(() => nativeToolCalls("interactions", { outputs: [] })).toThrow("missing");
  });
  it.each(["messages", "interactions"] as const)("binds %s manual results to an exact submitted declaration and restricts undeclared calls to decline", (endpoint) => {
    const tool = endpoint === "messages" ? { name: "lookup", input_schema: { type: "object" } } : { type: "function", name: "lookup", parameters: { type: "object" } };
    const payload = endpoint === "messages" ? { content: [{ type: "tool_use", id: "call", name: "lookup", input: {} }] } : { steps: [{ type: "function_call", id: "call", name: "lookup", arguments: {} }] };
    const calls = nativeToolCalls(endpoint, payload, [tool]), results = [{ id: "call", text: "Result", declined: false }];
    expect(calls[0].issue).toBeUndefined(); expect(() => validateNativeContinuation(endpoint, calls, results, [tool])).not.toThrow();
    for (const tools of [[], [tool, tool], [{ ...tool, description: "Changed" }]]) expect(() => validateNativeContinuation(endpoint, calls, results, tools)).toThrow("definition changed");
    for (const tools of [[], [tool, tool]]) {
      const unbound = nativeToolCalls(endpoint, payload, tools); expect(unbound[0].issue).toContain("only be declined");
      expect(() => validateNativeContinuation(endpoint, unbound, results, tools)).toThrow("only be declined");
      expect(() => validateNativeContinuation(endpoint, unbound, [{ ...results[0], declined: true }], tools)).not.toThrow();
    }
  });
  it("rejects invalid native attachments before request transport", () => {
    const file = { filename: "input.png", media_type: "image/png", data_base64: "AA==" };
    for (const files of [[{ ...file, filename: "../input.png" }], [{ ...file, media_type: "image/svg+xml" }], [{ ...file, data_base64: "invalid" }], Array.from({ length: 6 }, () => file)]) expect(() => nativeUserContent("messages", "Inspect", files)).toThrow("Invalid native attachment");
    expect(() => nativeUserContent("interactions", "Inspect", [{ ...file, data_base64: "AAAA".repeat(Math.ceil(8 * 1024 * 1024 / 3) + 1) }])).toThrow("8 MiB");
    expect(() => nativeUserContent("messages", "Inspect", [{ ...file, data_base64: "AAAA".repeat(350000) }])).not.toThrow();
  });
  it("checks result bounds and emits protocol-specific refusal results", () => {
    const result = { id: "call", text: "Declined", declined: true };
    expect(nativeToolContent("messages", [result])).toEqual([{ type: "tool_result", tool_use_id: "call", content: "Declined", is_error: true }]);
    expect(nativeToolContent("interactions", [result])).toEqual([{ type: "function_call_output", call_id: "call", output: '{"error":"Declined"}' }]);
    expect(() => nativeToolContent("messages", [result, result])).toThrow("distinct");
    expect(() => nativeToolContent("messages", [{ ...result, text: "я".repeat(65537) }])).toThrow("128 KiB");
    expect(() => nativeToolContent("messages", [{ ...result, text: "  " }])).toThrow("non-empty");
  });
  it("replays interaction model/function steps and refuses unknown non-replayable steps", () => {
    const turns: NativeTurn[] = [{ role: "user", content: "First" }, { role: "assistant", content: [{ type: "thought", content: [{ type: "text", text: "Display only" }] }, { type: "model_output", content: [{ type: "text", text: "Answer" }] }, { type: "function_call", id: "call", name: "lookup", arguments: { count: 1 } }] }, { role: "tool", content: [{ type: "function_call_output", call_id: "call", output: "Result" }] }];
    expect(nativeHistory("interactions", turns)).toEqual([{ role: "user", content: "First" }, { role: "assistant", content: [{ type: "output_text", text: "Answer" }] }, { type: "function_call", call_id: "call", name: "lookup", arguments: '{"count":1}' }, { type: "function_call_output", call_id: "call", output: "Result" }]);
    expect(() => nativeHistory("interactions", [{ role: "assistant", content: [{ type: "unsupported" }] }])).toThrow("cannot be replayed");
    expect(() => nativeHistory("interactions", [{ role: "assistant", content: [{ type: "model_output", content: [{ type: "image", data: "AA==" }] }] }])).toThrow("cannot be replayed as text");
  });
  it("evicts whole native conversation groups and maps tool results back to Messages user blocks", () => {
    const groups: NativeTurn[] = Array.from({ length: 11 }, (_, i) => [{ role: "user" as const, content: `Prompt ${i}` }, { role: "assistant" as const, content: [{ type: "tool_use", id: `call-${i}`, name: "lookup", input: {} }] }, { role: "tool" as const, content: [{ type: "tool_result", tool_use_id: `call-${i}`, content: "Result" }] }, { role: "assistant" as const, content: [{ type: "text", text: "Answer" }] }]).flat();
    const retained = retainConversation(groups);
    expect(retained.dropped).toBe(4); expect(retained.turns).toHaveLength(40); expect(retained.turns[0].content).toBe("Prompt 1");
    expect(nativeHistory("messages", retained.turns)[2]).toEqual({ role: "user", content: [{ type: "tool_result", tool_use_id: "call-1", content: "Result" }] });
  });
});
