import { exactObjectText, parseExactObject, parseNativeJSON, stringifyExactJSON } from "./exactJSON";

describe("native JSON precision", () => {
  const args = '{ "id":9007199254740993,"amount":0.1234567890123456789012345,"nested":[{"negative":-9007199254740993}],"text":"quote \\\" and \\u007d" }';
  it.each(["tool_use", "function_call"])("preserves %s numeric literals and escaping through response parsing and request encoding", (type) => {
    const field = type === "tool_use" ? "input" : "arguments";
    const source = `{"steps":[{"type":"${type}","name":"query","id":"call","${field}":${args}}],"usage":{"input_tokens":0}}`;
    const parsed = parseNativeJSON(source) as { steps: Record<string, unknown>[] };
    expect(exactObjectText(parsed.steps[0][field])).toBe(args);
    const wire = stringifyExactJSON({ history: parsed.steps, result: "<not-json>" });
    expect(wire).toContain(`"${field}":${args}`); expect(JSON.parse(wire)).toMatchObject({ result: "<not-json>" });
    expect(Object.keys(parsed.steps[0][field] as object)).not.toContain("native tool argument JSON");
  });
  it("preserves root streamed arguments, nested native envelopes and duplicate-member last-value semantics", () => {
    expect(exactObjectText(parseExactObject(args))).toBe(args);
    const parsed = parseNativeJSON(`{"interaction":{"steps":[{"type":"function_call","arguments":{},"arguments":${args}}]}}`) as { interaction: { steps: Record<string, unknown>[] } };
    expect(exactObjectText(parsed.interaction.steps[0].arguments)).toBe(args);
    expect(parseNativeJSON('[null,true,1,"a\\\\\\\"b",{},[]]')).toEqual(JSON.parse('[null,true,1,"a\\\\\\\"b",{},[]]'));
    expect(stringifyExactJSON({ omitted: undefined, list: [undefined, null, false, 0] })).toBe('{"list":[null,null,false,0]}');
  });
  it("rejects changed reviewed arguments, malformed JSON, excessive depth and cycles", () => {
    const value = parseExactObject(args) as Record<string, unknown>; value.id = 2;
    expect(() => stringifyExactJSON(value)).toThrow("arguments changed");
    expect(() => parseNativeJSON('{"broken":')).toThrow();
    expect(() => parseNativeJSON('"' + "я".repeat(1024 * 1024 + 1) + '"')).toThrow("2 MiB");
    expect(stringifyExactJSON(new Array(2))).toBe("[null,null]");
    expect(() => parseNativeJSON("[".repeat(130) + "0" + "]".repeat(130))).toThrow("depth limit");
    const cycle: Record<string, unknown> = {}; cycle.self = cycle;
    expect(() => stringifyExactJSON(cycle)).toThrow("circular");
    expect(() => stringifyExactJSON(undefined)).toThrow("must contain JSON");
    expect(() => stringifyExactJSON(Array.from({ length: 1 }, () => cycle))).toThrow("circular");
    let deep: unknown = 0; for (let i = 0; i < 130; i++) deep = [deep];
    expect(() => stringifyExactJSON(deep)).toThrow("depth limit");
    expect(parseExactObject("[]")).toEqual([]); expect(exactObjectText({ id: 1 })).toBe('{"id":1}');
  });
});
