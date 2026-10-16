import { APIClient } from "../../api/client";
import { agentApprovalRequest, agentRequest, runAgentRequest, type AgentRun } from "./agents";
import { playgroundConnection } from "./requests";

const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const task = { id: "task-one", contextId: "ctx-one", status: { state: "TASK_STATE_WORKING" }, artifacts: [] };
const initial = { task };
const artifact = (text: string, append = false) => ({ artifactUpdate: { taskId: task.id, contextId: task.contextId, artifact: { artifactId: "artifact-one", parts: [{ text }] }, append, lastChunk: false } });
const status = (state = "TASK_STATE_COMPLETED") => ({ statusUpdate: { taskId: task.id, contextId: task.contextId, status: { state } } });
const wire = (id: string, result: unknown) => `data: ${JSON.stringify({ jsonrpc: "2.0", id, result })}\n\n`;
const sse = (id: string, results: unknown[]) => new Response(results.map((result) => wire(id, result)).join(""), { headers: { "Content-Type": "text/event-stream" } });

describe("agent task streaming", () => {
  it("keeps JSON the default and explicitly selects streaming for messages and approvals", () => {
    expect(agentRequest("writer", "Hello").body.method).toBe("SendMessage");
    const request = agentRequest("writer", "Hello", undefined, [], true);
    expect(request.body.method).toBe("SendStreamingMessage"); expect(request.headers).toEqual({ "A2A-Version": "1.0" });
    const review = { id: task.id, contextID: task.contextId, state: "TASK_STATE_INPUT_REQUIRED", approval: { id: "approval-one", calls: [{ id: "call-one", server: "server", tool: "lookup", arguments: {} }] } };
    expect(agentApprovalRequest("writer", review, [{ call_id: "call-one", approved: false }], true).body).toMatchObject({ method: "SendStreamingMessage", params: { message: { taskId: task.id, contextId: task.contextId, metadata: { ai_gateway_tool_approval: { choices: [{ call_id: "call-one", approved: false }] } } } } });
  });
  it("publishes independent task snapshots, assembles deltas and preserves missing metrics", async () => {
    const request = agentRequest("writer", "Hello", undefined, [], true), updates: AgentRun[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(sse(request.body.id, [initial, artifact("Hello"), artifact(" world", true), status()]));
    const result = await runAgentRequest(connection(), request, new AbortController().signal, (value) => updates.push(value));
    expect(updates.map((value) => value.text)).toEqual(["", "Hello", "Hello world", "Hello world"]);
    expect(updates[0].task?.state).toBe("TASK_STATE_WORKING"); expect(result.task?.state).toBe("TASK_STATE_COMPLETED"); expect(result.text).toBe("Hello world");
    expect(result).not.toHaveProperty("usage"); expect(result).not.toHaveProperty("firstTokenMS");
    expect(new Headers(mock.mock.calls[0][1]?.headers).get("Accept")).toBe("text/event-stream");
  });
  it("publishes a task before the stream closes and retains its identity on incomplete EOF", async () => {
    const request = agentRequest("writer", "Hello", undefined, [], true), updates: AgentRun[] = [];
    let controller: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(body, { headers: { "Content-Type": "text/event-stream" } }));
    let observed: () => void = () => {};
    const ready = new Promise<void>((resolve) => { observed = resolve; });
    const result = runAgentRequest(connection(), request, new AbortController().signal, (value) => { updates.push(value); observed(); });
    controller!.enqueue(new TextEncoder().encode(wire(request.body.id, initial))); await ready;
    expect(updates[0].task).toMatchObject({ id: task.id, state: "TASK_STATE_WORKING" });
    controller!.enqueue(new TextEncoder().encode(wire(request.body.id, artifact("Partial")))); controller!.close();
    await expect(result).rejects.toThrow("Refresh the known task"); expect(updates.at(-1)?.text).toBe("Partial");
  });
  it.each([
    { artifactUpdate: { ...artifact("Foreign").artifactUpdate, taskId: "foreign" } },
    { statusUpdate: { ...status().statusUpdate, contextId: "foreign" } },
    initial,
    artifact("Unknown append", true),
    { statusUpdate: { ...status().statusUpdate, status: { state: "unknown" } } },
    { artifactUpdate: { ...artifact("Foreign").artifactUpdate, artifact: { artifactId: "bad/id", parts: [] } } },
  ])("rejects invalid updates without publishing their content %j", async (invalid) => {
    const request = agentRequest("writer", "Hello", undefined, [], true), updates: AgentRun[] = [];
    vi.spyOn(globalThis, "fetch").mockResolvedValue(sse(request.body.id, [initial, invalid]));
    await expect(runAgentRequest(connection(), request, new AbortController().signal, (value) => updates.push(value))).rejects.toThrow();
    expect(updates).toHaveLength(1); expect(updates[0].text).toBe("");
  });
  it("rejects foreign RPC IDs, malformed events and data after terminal status", async () => {
    const request = agentRequest("writer", "Hello", undefined, [], true), mock = vi.spyOn(globalThis, "fetch");
    for (const response of [sse("foreign", [initial]), new Response("data: invalid\n\n", { headers: { "Content-Type": "text/event-stream" } }), sse(request.body.id, [initial, status(), artifact("Late")])]) {
      mock.mockResolvedValueOnce(response); await expect(runAgentRequest(connection(), request, new AbortController().signal)).rejects.toThrow();
    }
  });
  it("keeps failed and replayed working tasks truthful without automatic inference retries", async () => {
    const request = agentRequest("writer", "Hello", undefined, [], true), mock = vi.spyOn(globalThis, "fetch");
    mock.mockResolvedValueOnce(sse(request.body.id, [initial, artifact("Partial"), status("TASK_STATE_FAILED")]));
    expect(await runAgentRequest(connection(), request, new AbortController().signal)).toMatchObject({ text: "Partial", task: { state: "TASK_STATE_FAILED" } });
    mock.mockResolvedValueOnce(sse(request.body.id, [initial, status("TASK_STATE_WORKING")]));
    expect((await runAgentRequest(connection(), request, new AbortController().signal)).task?.state).toBe("TASK_STATE_WORKING"); expect(mock).toHaveBeenCalledTimes(2);
  });
  it("preserves explicit approvals and accepts a validated JSON response without reissuing transport", async () => {
    const request = agentRequest("writer", "Hello", undefined, [], true);
    const review = { state: "TASK_STATE_INPUT_REQUIRED", message: { role: "ROLE_AGENT", taskId: task.id, contextId: task.contextId, parts: [{ text: "Review tools" }], metadata: { ai_gateway_tool_approval: { approval_id: "approval-one", calls: [{ call_id: "call-one", server_id: "server", tool_name: "lookup", arguments: { count: 2 } }] } } } };
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(sse(request.body.id, [initial, { statusUpdate: { taskId: task.id, contextId: task.contextId, status: review } }]));
    const result = await runAgentRequest(connection(), request, new AbortController().signal);
    expect(result.task?.approval?.calls[0]).toMatchObject({ id: "call-one", arguments: { count: 2 } }); expect(mock).toHaveBeenCalledOnce();
    const next = agentApprovalRequest("writer", result.task!, [{ call_id: "call-one", approved: false }], true);
    mock.mockResolvedValueOnce(new Response(JSON.stringify({ jsonrpc: "2.0", id: next.body.id, result: { task: { ...task, status: { state: "TASK_STATE_COMPLETED" } } } }), { headers: { "Content-Type": "application/json" } }));
    expect((await runAgentRequest(connection(), next, new AbortController().signal)).task?.state).toBe("TASK_STATE_COMPLETED"); expect(mock).toHaveBeenCalledTimes(2);
  });
  it("rejects cancelled late events and oversized transport, canceling the reader", async () => {
    const request = agentRequest("writer", "Hello", undefined, [], true), controller = new AbortController(), updates: AgentRun[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(sse(request.body.id, [initial, artifact("Late"), status()]));
    await expect(runAgentRequest(connection(), request, controller.signal, (value) => { updates.push(value); controller.abort(); })).rejects.toThrow("cancelled");
    expect(updates).toHaveLength(1);
    mock.mockResolvedValueOnce(sse(request.body.id, [{ ...initial, task: { ...task, large: "x".repeat(4 * 1024 * 1024) } }]));
    await expect(runAgentRequest(connection(), request, new AbortController().signal)).rejects.toThrow("Playground size limit");
  });
});
