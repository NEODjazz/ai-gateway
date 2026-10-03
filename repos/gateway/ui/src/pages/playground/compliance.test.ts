import { APIClient } from "../../api/client";
import { complianceCSV, parseComplianceCSV, runCompliance, starterCases } from "./compliance";
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
