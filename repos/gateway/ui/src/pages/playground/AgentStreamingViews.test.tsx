import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { AgentExecution } from "./AgentExecution";
import { ComparePlayground } from "./ComparePlayground";
import { EndpointPlayground } from "./EndpointPlayground";
import { playgroundConnection } from "./requests";
import type { AgentProfile } from "./agents";

const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const profile: AgentProfile = { id: "writer", name: "Writer", model: "model", instructions_configured: true, tool_policy_id: "policy", allowed_tools: ["lookup"], max_tool_calls: 2, max_iterations: 2, enabled: true, execution_supported: true };
const task = { id: "task-one", contextId: "ctx-one", status: { state: "TASK_STATE_WORKING" }, artifacts: [] };
const artifact = (text: string) => ({ artifactUpdate: { taskId: task.id, contextId: task.contextId, artifact: { artifactId: "answer", parts: [{ text }] }, append: false, lastChunk: true } });
const status = (state = "TASK_STATE_COMPLETED") => ({ statusUpdate: { taskId: task.id, contextId: task.contextId, status: { state } } });
const wire = (id: string, result: unknown) => `data: ${JSON.stringify({ jsonrpc: "2.0", id, result })}\n\n`;
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
const sse = (id: string, results: unknown[]) => new Response(results.map((result) => wire(id, result)).join(""), { headers: { "Content-Type": "text/event-stream" } });
function heldStream() {
  let reader: ReadableStreamDefaultController<Uint8Array>, id = "";
  const body = new ReadableStream<Uint8Array>({ start(value) { reader = value; } });
  return { response: (rpcID: string) => { id = rpcID; return new Response(body, { headers: { "Content-Type": "text/event-stream" } }); },
    send: async (result: unknown) => act(async () => reader!.enqueue(new TextEncoder().encode(wire(id, result)))),
    close: async () => act(async () => reader!.close()) };
}
const refreshed = (id: string) => json({ jsonrpc: "2.0", id, result: { ...task, status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ artifactId: "answer", parts: [{ text: "Recovered answer" }] }] } });

describe("streaming agent workspaces", () => {
  it("shows Chat task before EOF, retains interrupted output and refreshes without duplicating history", async () => {
    const held = heldStream(), mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const body = JSON.parse(String(options?.body)); return body.method === "GetTask" ? refreshed(body.id) : held.response(body.id);
    });
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="chat" />);
    await userEvent.click(screen.getByLabelText("Stream agent task")); await userEvent.type(screen.getByLabelText("Agent prompt"), "Keep this prompt");
    await userEvent.click(screen.getByRole("button", { name: "Send to agent" })); await held.send({ task });
    expect(screen.getByRole("status")).toHaveTextContent("Task task-one · TASK_STATE_WORKING"); expect(screen.getByRole("button", { name: "Refresh agent task" })).toBeDisabled();
    await held.send(artifact("Partial answer")); expect(screen.getByText("Partial answer")).toBeInTheDocument(); await held.close();
    expect(await screen.findByRole("alert")).toHaveTextContent("Refresh the known task"); expect(screen.getByLabelText("Agent prompt")).toBeDisabled(); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Refresh agent task" })); await screen.findByText("Recovered answer");
    expect(within(screen.getByRole("region", { name: "Agent conversation history" })).getAllByRole("article")).toHaveLength(2);
    expect(screen.queryByText("Partial answer")).not.toBeInTheDocument(); expect(screen.getByRole("status")).toHaveTextContent("TASK_STATE_COMPLETED");
    expect(JSON.parse(String(mock.mock.calls[0][1]?.body)).method).toBe("SendStreamingMessage"); expect(JSON.parse(String(mock.mock.calls[1][1]?.body)).method).toBe("GetTask");
    expect(screen.getByText(/Token usage and first-token timing are unavailable/)).toBeInTheDocument();
  });
  it("streams explicit decisions only after Continue, preserving task identity in Chat", async () => {
    const review = { state: "TASK_STATE_INPUT_REQUIRED", message: { role: "ROLE_AGENT", taskId: task.id, contextId: task.contextId, parts: [{ text: "Review tools" }], metadata: { ai_gateway_tool_approval: { approval_id: "approval-one", calls: [{ call_id: "call-one", server_id: "server", tool_name: "lookup", arguments: { count: 2 } }] } } } };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const body = JSON.parse(String(options?.body)); return sse(body.id, body.params.message.metadata ? [{ task }, artifact("Reviewed answer"), status()] : [{ task }, { statusUpdate: { taskId: task.id, contextId: task.contextId, status: review } }]);
    });
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="chat" />);
    await userEvent.click(screen.getByLabelText("Stream agent task")); await userEvent.type(screen.getByLabelText("Agent prompt"), "Review lookup"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" }));
    const approval = within(await screen.findByRole("region", { name: "Agent tool approvals" }));
    await userEvent.click(approval.getByLabelText("Agent tool approvals decision 1")); await userEvent.click(screen.getByRole("option", { name: "Decline" })); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(approval.getByRole("button", { name: "Continue agent tool approvals" })); await screen.findByText("Reviewed answer");
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).toMatchObject({ method: "SendStreamingMessage", params: { message: { taskId: task.id, contextId: task.contextId, metadata: { ai_gateway_tool_approval: { choices: [{ call_id: "call-one", approved: false }] } } } } });
    expect(within(screen.getByRole("region", { name: "Agent conversation history" })).getAllByRole("article")).toHaveLength(2);
  });
  it("keeps one Batch row per prompt and a known task after incomplete EOF", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const body = JSON.parse(String(options?.body)); return body.method === "GetTask" ? refreshed(body.id) : sse(body.id, [{ task }, artifact("Partial batch answer")]);
    });
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="batch" />);
    await userEvent.click(screen.getByLabelText("Stream agent task")); await userEvent.type(screen.getByLabelText("Agent batch prompts"), "Batch prompt"); await userEvent.click(screen.getByRole("button", { name: "Run agent tests" }));
    await screen.findByText(/Agent stream ended/); expect(screen.getByText("Partial batch answer")).toBeInTheDocument(); expect(screen.getAllByRole("article")).toHaveLength(1); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Refresh test 1 task" })); await screen.findByText("Recovered answer"); expect(screen.getAllByRole("article")).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveTextContent("1/1 responses received · 1 completed");
  });
  it("does not publish old Chat events after credentials change", async () => {
    const held = heldStream(); vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => held.response(JSON.parse(String(options?.body)).id));
    const view = render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="chat" />);
    await userEvent.click(screen.getByLabelText("Stream agent task")); await userEvent.type(screen.getByLabelText("Agent prompt"), "Old prompt"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" })); await held.send({ task });
    view.rerender(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="chat" />);
    await held.send(artifact("Late private answer")); expect(screen.queryByText("Late private answer")).not.toBeInTheDocument(); expect(screen.queryByText(/Task task-one/)).not.toBeInTheDocument();
  });
  it("keeps a failed stream next to a successful model and recovers through GetTask", async () => {
    const held = heldStream(), mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      const body = JSON.parse(String(options?.body)); if (String(path).startsWith("/a2a/")) return body.method === "GetTask" ? refreshed(body.id) : held.response(body.id);
      return json({ choices: [{ message: { role: "assistant", content: "Model succeeded" } }], usage: { prompt_tokens: 4, completion_tokens: 2 } });
    });
    render(<ComparePlayground connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
    const panel = within(screen.getByRole("region", { name: "Comparison 2" })); await userEvent.click(panel.getByLabelText("Comparison type 2")); await userEvent.click(screen.getByRole("option", { name: "Saved agent" })); await userEvent.type(panel.getByLabelText("Agent 2"), "writer");
    await userEvent.type(screen.getByLabelText("Comparison prompt"), "Shared prompt"); await userEvent.click(screen.getByRole("button", { name: "Compare models" })); await held.send({ task }); await held.send(artifact("Partial comparison answer"));
    expect(panel.getByText("Partial comparison answer")).toBeInTheDocument(); await held.close(); await screen.findByText("Model succeeded"); expect(await screen.findByRole("alert")).toHaveTextContent("Refresh the known task"); expect(mock).toHaveBeenCalledTimes(2);
    expect(screen.getByLabelText("Comparison prompt")).toBeDisabled(); expect(panel.getAllByText("Not reported")).toHaveLength(2);
    await userEvent.click(panel.getByRole("button", { name: "Refresh comparison 2 task" })); await screen.findByText("Recovered answer"); expect(screen.getByText("Model succeeded")).toBeInTheDocument(); expect(screen.getByLabelText("Comparison prompt")).toBeEnabled();
    expect(JSON.parse(String(mock.mock.calls.find(([path]) => path === "/a2a/writer")?.[1]?.body)).method).toBe("SendStreamingMessage");
  });
  it("retains an endpoint task after local stop and rejects late events", async () => {
    const held = heldStream(), mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => { const body = JSON.parse(String(options?.body)); return body.method === "GetTask" ? refreshed(body.id) : held.response(body.id); });
    render(<EndpointPlayground endpoint="a2a" connection={connection()} models={[]} connectionChanged={false} connectionControls={null} />);
    await userEvent.type(screen.getByLabelText("Agent ID"), "writer"); await userEvent.click(screen.getByLabelText("Stream endpoint agent task")); await userEvent.type(screen.getByLabelText("Endpoint input"), "Endpoint prompt"); await userEvent.click(screen.getByRole("button", { name: "Run endpoint request" })); await held.send({ task }); await held.send(artifact("Endpoint partial"));
    expect(screen.getByText("Endpoint partial")).toBeInTheDocument(); await userEvent.click(screen.getByRole("button", { name: "Stop endpoint request" })); await held.send(artifact("Late endpoint answer"));
    expect(await screen.findByRole("alert")).toHaveTextContent("cancelled"); expect(screen.queryByText("Late endpoint answer")).not.toBeInTheDocument(); expect(screen.getByLabelText("Endpoint input")).toBeDisabled(); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Refresh endpoint task" })); await screen.findByText("Recovered answer"); expect(within(screen.getByRole("region", { name: "Agent conversation history" })).getAllByRole("article")).toHaveLength(2); expect(screen.getByLabelText("Endpoint input")).toBeEnabled();
    expect(screen.getByText("First token").nextElementSibling).toHaveTextContent("—"); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("—");
  });
  it("exports the selected streaming method without credentials in Connect", async () => {
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="connect" />);
    await userEvent.click(screen.getByLabelText("Stream agent task")); await userEvent.click(screen.getByRole("button", { name: "Get connection code" }));
    const code = screen.getByLabelText("Request code"); expect(code).toHaveTextContent("SendStreamingMessage"); expect(code).not.toHaveTextContent("test-key");
  });
});
