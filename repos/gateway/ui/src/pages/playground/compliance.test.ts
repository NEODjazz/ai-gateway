import { APIClient } from "../../api/client";
import { complianceCSV, compliancePolicies, type ComplianceResult, parseComplianceCSV, runCompliance, starterCases } from "./compliance";
import { playgroundConnection } from "./requests";

describe("Policy test cases", () => {
  it("round-trips CSV with commas, quotes and multiline prompts", () => {
    const cases = [{ ...starterCases[0], prompt: 'First, "quoted"\nSecond line' }];
    expect(parseComplianceCSV(complianceCSV(cases))).toEqual([{ ...cases[0], id: expect.any(String) }]);
  });
  it.each([
    ['prompt,expected\nhello,allow', "CSV columns"],
    ['category,framework,prompt,expected\na,b,"unterminated,allow', "Unterminated"],
    ['category,framework,prompt,expected\na,b,"hello"x,allow', "Unexpected text"],
    ['category,framework,prompt,expected\na,b,hello,pass', "expected must"],
    ['category,framework,prompt,expected\na,b,,allow', "prompt must"],
    ['category,framework,prompt,expected\na,b,hello,allow,extra', "field count"]
  ])("rejects invalid CSV %s", (csv, error) => { expect(() => parseComplianceCSV(csv)).toThrow(error); });
  it("bounds import size and case count", () => {
    expect(() => parseComplianceCSV("x".repeat(1024 * 1024 + 1))).toThrow("1 MiB");
    expect(() => parseComplianceCSV("category,framework,prompt,expected\n" + "a,b,hello,allow\n".repeat(501))).toThrow("500");
  });
  it("protects result CSV against spreadsheet formulas", () => {
    expect(complianceCSV([{ ...starterCases[0], prompt: "=formula" }], [{ id: starterCases[0].id, status: "failed", error: "@formula", latencyMS: 1 }])).toContain('"\'=formula"');
  });
});

describe("Policy test execution", () => {
  const connection = () => playgroundConnection(new APIClient(() => "policy-test-key"), "session", "", "");
  it("distinguishes allowed, blocked, failed and unexpected outcomes without model generation", async () => {
    let index = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
      index++;
      return index === 3 ? new Response('{"error":{"message":"Scanner offline"}}', { status: 503 }) : new Response(JSON.stringify({ allowed: index === 1, checks: { dlp: index === 1 ? "allow" : "block" }, execution_id: `execution-${index}` }));
    });
    const results: unknown[] = [];
    await runCompliance(connection(), starterCases, "strict", "model-a", new AbortController().signal, (result) => results.push(result));
    expect(results).toEqual([expect.objectContaining({ status: "allowed", matched: true }), expect.objectContaining({ status: "blocked", matched: true }), expect.objectContaining({ status: "failed", error: "Scanner offline" })]);
    expect(mock.mock.calls.every(([path]) => path === "/guardrails/apply_guardrail")).toBe(true);
    expect(JSON.parse(String(mock.mock.calls[0][1]?.body))).toEqual({ guardrail_name: "strict", text: starterCases[0].prompt, model: "model-a" });
  });
  it("limits concurrency to three and cancels pending cases without additional calls", async () => {
    const controller = new AbortController(), results: unknown[] = [];
    const deferred: ((response: Response) => void)[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((resolve) => deferred.push(resolve)));
    const running = runCompliance(connection(), Array.from({ length: 10 }, (_, index) => ({ ...starterCases[0], id: `case-${index}` })), "strict", "", controller.signal, (result) => results.push(result));
    expect(mock).toHaveBeenCalledTimes(3); controller.abort(); deferred.forEach((resolve) => resolve(new Response('{"allowed":true}'))); await running;
    expect(mock).toHaveBeenCalledTimes(3); expect(results).toHaveLength(10); expect(results.every((result) => (result as { status: string }).status === "cancelled")).toBe(true);
  });
  it("does not convert malformed scanner responses to an allow or block result", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"allowed":"false"}'));
    const results: unknown[] = [];
    await runCompliance(connection(), [starterCases[0]], "strict", "", new AbortController().signal, (result) => results.push(result));
    expect(results).toEqual([expect.objectContaining({ status: "failed", error: "Guardrail returned an invalid result." })]);
  });
});


describe("Multiple policy checks", () => {
  const connection = () => playgroundConnection(new APIClient(() => "policy-test-key"), "session", "", "");
  it.each(["", "strict,", "strict,invalid name", "strict,strict", "a,b,c,d,e"])("rejects invalid selection %s before transport", async (selection) => {
    const transport = vi.spyOn(globalThis, "fetch");
    await expect(runCompliance(connection(), starterCases, selection, "", new AbortController().signal, () => {})).rejects.toThrow();
    expect(transport).not.toHaveBeenCalled();
  });
  it("validates the whole suite before dispatch", async () => {
    const transport = vi.spyOn(globalThis, "fetch");
    await expect(runCompliance(connection(), [...starterCases, { ...starterCases[0], id: "bad", prompt: " " }], ["strict", "privacy"], "", new AbortController().signal, () => {})).rejects.toThrow("prompt");
    await expect(runCompliance(connection(), [starterCases[0], starterCases[0]], "strict", "", new AbortController().signal, () => {})).rejects.toThrow("IDs");
    expect(transport).not.toHaveBeenCalled();
    expect(compliancePolicies(" strict, privacy ")).toEqual(["strict", "privacy"]);
  });
  it("keeps decisions, failures and execution IDs separate for every case and policy", async () => {
    const transport = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const body = JSON.parse(String(options?.body));
      if (body.guardrail_name === "offline") return new Response('{"error":{"message":"Scanner offline"}}', { status: 503 });
      return new Response(JSON.stringify({ allowed: body.guardrail_name === "allow", execution_id: `${body.guardrail_name}-${body.text}`, checks: { scanner: body.guardrail_name } }));
    });
    const results: ComplianceResult[] = [];
    await runCompliance(connection(), starterCases.slice(0, 2), ["allow", "block", "offline"], "model-a", new AbortController().signal, (result) => results.push(result));
    expect(results).toHaveLength(6);
    for (const item of starterCases.slice(0, 2)) {
      expect(results.filter((result) => result.id === item.id)).toEqual(expect.arrayContaining([
        expect.objectContaining({ policy: "allow", status: "allowed", matched: item.expected === "allow", executionID: `allow-${item.prompt}` }),
        expect.objectContaining({ policy: "block", status: "blocked", matched: item.expected === "block", executionID: `block-${item.prompt}` }),
        expect.objectContaining({ policy: "offline", status: "failed", error: "Scanner offline" })
      ]));
    }
    expect(transport.mock.calls.every(([path, options]) => path === "/guardrails/apply_guardrail" && JSON.parse(String(options?.body)).model === "model-a")).toBe(true);
    const csv = complianceCSV(starterCases.slice(0, 2), results);
    expect(csv.split("\r\n")[0]).toBe('"category","framework","prompt","expected","status","matched","error","execution_id","latency_ms","policy"');
    expect(csv.split("\r\n")).toHaveLength(8);
    expect(csv.match(/"offline"/g)).toHaveLength(2);
    expect(csv.match(/"Scanner offline"/g)).toHaveLength(2);
  });
  it("bounds concurrency globally across the product and does not dispatch cancelled checks", async () => {
    const controller = new AbortController(), results: ComplianceResult[] = [], deferred: ((response: Response) => void)[] = [];
    const transport = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((resolve) => deferred.push(resolve)));
    const running = runCompliance(connection(), starterCases, ["one", "two", "three", "four"], "", controller.signal, (result) => results.push(result));
    expect(transport).toHaveBeenCalledTimes(3);
    controller.abort(); deferred.forEach((resolve) => resolve(new Response('{"allowed":true}'))); await running;
    expect(transport).toHaveBeenCalledTimes(3);
    expect(results).toHaveLength(12);
    expect(new Set(results.map((result) => JSON.stringify([result.id, result.policy]))).size).toBe(12);
    expect(results.every((result) => result.status === "cancelled")).toBe(true);
    expect(results.filter((result) => result.latencyMS === undefined)).toHaveLength(9);
  });
});
