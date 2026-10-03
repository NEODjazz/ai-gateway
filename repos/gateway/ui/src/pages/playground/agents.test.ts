import { APIClient } from "../../api/client";
import { agentBatchCSV, agentBatchPrompts, agentBody, agentDraft, agentRequest, agentTaskRequest, emptyAgentDraft, runAgentBatch, runAgentRequest, type AgentBatchResult, type AgentProfile } from "./agents";
import { playgroundConnection } from "./requests";

const connection = () => playgroundConnection(new APIClient(() => "test-agent-key"), "session", "", "");
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
const profile: AgentProfile = { id: "writer", name: "Writer", model: "model", instructions_configured: true, generation: { temperature: 0, max_output_tokens: 400 }, tool_policy_id: "policy", allowed_tools: ["lookup"], max_tool_calls: 3, max_iterations: 2, tags: ["team"], enabled: true, execution_supported: true };
function response(id: string, text: string) { return json({ jsonrpc: "2.0", id, result: { message: { role: "ROLE_AGENT", parts: [{ text }] } } }); }

describe("saved agent requests", () => {
  it("round-trips explicit instructions and generation, and explicitly clears settings", () => {
    const draft = agentDraft(profile, "Saved instructions");
    expect(agentBody(draft)).toMatchObject({ instructions: "Saved instructions", generation: { temperature: 0, max_output_tokens: 400 }, max_iterations: 2, tags: ["team"] });
    expect(agentBody({ ...draft, instructions: "", temperature: "", maxTokens: "" })).toMatchObject({ instructions: "", generation: {} });
    expect(agentBody({ ...emptyAgentDraft, id: "a", name: "Agent", model: "model", policy: "policy" })).not.toHaveProperty("allowed_tools");
  });
  it.each([{ id: "bad/id" }, { instructions: "x".repeat(65537) }, { instructions: "x\0y" }, { instructions: "inline", template: "template" }, { temperature: "NaN" }, { temperature: "3" }, { maxTokens: "0" }, { maxTokens: "1000001" }, { iterations: "51" }, { iterations: "" }])("rejects invalid configuration %j", (change) => {
    expect(() => agentBody({ ...agentDraft(profile, ""), ...change })).toThrow();
  });
  it("builds server task continuity and inline attachments without client settings", () => {
    const request = agentRequest("writer", "Follow up", { id: "task-one", contextID: "ctx-one", state: "TASK_STATE_COMPLETED" }, [{ filename: "brief.pdf", media_type: "application/pdf", data_base64: "cGRm" }]);
    expect(request.headers).toEqual({ "A2A-Version": "1.0" });
    expect(request.body.params.message).toMatchObject({ taskId: "task-one", contextId: "ctx-one", parts: [{ text: "Follow up" }, { raw: "cGRm", filename: "brief.pdf", mediaType: "application/pdf" }] });
    expect(request.body.params).not.toHaveProperty("instructions");
    expect(() => agentRequest("writer", "Prompt", { id: "task-one", contextID: "ctx-one", state: "TASK_STATE_WORKING" })).toThrow("resolve");
    expect(agentTaskRequest("writer", { id: "task-one", contextID: "ctx-one", state: "TASK_STATE_WORKING" }, "GetTask").body.params).toEqual({ tenant: "writer", id: "task-one" });
  });
  it("validates JSON-RPC identity, errors, task states and latest artifacts", async () => {
    const request = agentRequest("writer", "Prompt"), signal = new AbortController().signal;
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ jsonrpc: "2.0", id: request.body.id, result: { task: { id: "task-one", contextId: "ctx-one", status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Old output" }] }, { parts: [{ text: "Latest output" }] }] } } }));
    const result = await runAgentRequest(connection(), request, signal);
    expect(result.text).toBe("Latest output"); expect(result.task?.state).toBe("TASK_STATE_COMPLETED");
    expect(new Headers(mock.mock.calls[0][1]?.headers).get("A2A-Version")).toBe("1.0");
    mock.mockResolvedValueOnce(json({ jsonrpc: "2.0", id: "different", result: {} }));
    await expect(runAgentRequest(connection(), request, signal)).rejects.toThrow("invalid agent response");
    mock.mockResolvedValueOnce(json({ jsonrpc: "2.0", id: request.body.id, error: { code: -32004, message: "Agent unavailable" } }));
    await expect(runAgentRequest(connection(), request, signal)).rejects.toThrow("Agent unavailable");
    mock.mockResolvedValueOnce(json({ jsonrpc: "2.0", id: request.body.id, result: { task: { id: "task", contextId: "ctx", status: { state: "unknown" } } } }));
    await expect(runAgentRequest(connection(), request, signal)).rejects.toThrow("invalid agent task");
    const get = agentTaskRequest("writer", result.task!, "GetTask");
    mock.mockResolvedValueOnce(json({ jsonrpc: "2.0", id: get.body.id, result: { id: "task-one", contextId: "ctx-one", status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Task refreshed" }] }] } }));
    expect((await runAgentRequest(connection(), get, signal)).text).toBe("Task refreshed");
  });
  it("rejects mismatched task or context IDs in agent continuation and refresh responses", async () => {
    const task = { id: "task-one", contextID: "context-one", state: "TASK_STATE_COMPLETED" };
    const mock = vi.spyOn(globalThis, "fetch");
    for (const request of [agentRequest("writer", "Next", task), agentTaskRequest("writer", task, "GetTask")]) {
      mock.mockResolvedValueOnce(json({ jsonrpc: "2.0", id: request.body.id, result: { task: { id: "task-one", contextId: "other-context", status: { state: "TASK_STATE_COMPLETED" } } } }));
      await expect(runAgentRequest(connection(), request, new AbortController().signal)).rejects.toThrow("different agent task");
    }
    const request = agentRequest("writer", "Prompt");
    await expect(runAgentRequest(connection(), { ...request, body: { ...request.body, params: [] } }, new AbortController().signal)).rejects.toThrow("Invalid agent RPC");
    expect(mock).toHaveBeenCalledTimes(2);
  });
  it("rejects malformed or oversized agent attachments before transport", () => {
    const file = { filename: "input.pdf", media_type: "application/pdf", data_base64: "AA==" };
    expect(() => agentRequest("writer", "", undefined, [{ ...file, filename: "../input.pdf" }])).toThrow("Invalid native attachment");
    expect(() => agentRequest("writer", "", undefined, [{ ...file, data_base64: "AAAA".repeat(Math.ceil(8 * 1024 * 1024 / 3) + 1) }])).toThrow("8 MiB");
  });
  it("bounds batch concurrency, keeps partial failures and cancels queued prompts", async () => {
    const deferred: { id: string; resolve: (response: Response) => void }[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation((_url, options) => new Promise<Response>((resolve) => deferred.push({ id: JSON.parse(String(options?.body)).id, resolve })));
    const controller = new AbortController(), results: AgentBatchResult[] = [];
    const run = runAgentBatch(connection(), "writer", ["First", "Second", "Third"], controller.signal, (item) => results.push(item));
    expect(mock).toHaveBeenCalledTimes(2); controller.abort();
    deferred.forEach((item) => item.resolve(response(item.id, "Late output")));
    await run; expect(mock).toHaveBeenCalledTimes(2); expect(results).toHaveLength(3); expect(results.every((item) => item.status === "cancelled")).toBe(true); expect(results.some((item) => item.text)).toBe(false);
    mock.mockImplementation(async (_url, options) => { const body = JSON.parse(String(options?.body)); return body.params.message.parts[0].text === "Bad" ? json({ jsonrpc: "2.0", id: body.id, error: { message: "Provider failed" } }) : response(body.id, "Success"); });
    const partial: AgentBatchResult[] = []; await runAgentBatch(connection(), "writer", ["Good", "Bad"], new AbortController().signal, (item) => partial.push(item));
    expect(partial.map((item) => item.status).sort()).toEqual(["completed", "failed"]);
  });
  it("bounds prompt suites and exports safe multiline CSV with unavailable fields left empty", () => {
    expect(agentBatchPrompts(" First\r\n\nSecond ")).toEqual(["First", "Second"]);
    expect(() => agentBatchPrompts(Array(21).fill("prompt").join("\n"))).toThrow("1–20");
    expect(() => agentBatchPrompts("x".repeat(65537))).toThrow("64 KiB");
    expect(agentBatchCSV([{ index: 1, prompt: "=SUM(1,2)", status: "failed", error: 'Line one\n"two"', latencyMS: 0 }])).toContain('"\'=SUM(1,2)"');
    expect(agentBatchCSV([{ index: 0, prompt: "Prompt", status: "completed", latencyMS: 3 }])).toContain('"completed","","","","","3"');
  });
});
