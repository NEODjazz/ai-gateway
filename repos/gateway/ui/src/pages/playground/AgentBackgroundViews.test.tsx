import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { AgentExecution } from "./AgentExecution";
import { ComparePlayground } from "./ComparePlayground";
import { EndpointPlayground } from "./EndpointPlayground";
import { agentApprovalRequest, agentRequest, type AgentProfile } from "./agents";
import { playgroundConnection } from "./requests";

const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const profile: AgentProfile = { id: "writer", name: "Writer", model: "model", instructions_configured: true, tool_policy_id: "policy", allowed_tools: ["lookup"], max_tool_calls: 2, max_iterations: 2, enabled: true, execution_supported: true };
const task = (state: string) => ({ id: "task-one", contextId: "ctx-one", status: state === "TASK_STATE_INPUT_REQUIRED" ? { state, message: { role: "ROLE_AGENT", taskId: "task-one", contextId: "ctx-one", parts: [{ text: "Review tools" }], metadata: { ai_gateway_tool_approval: { approval_id: "approval", calls: [{ call_id: "call", server_id: "server", tool_name: "lookup", arguments: { count: 2 } }] } } } } : { state }, artifacts: state === "TASK_STATE_COMPLETED" ? [{ artifactId: "answer", parts: [{ text: "Background final answer" }] }] : [] });
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });

describe("background agent Playground controls", () => {
  it.each(["chat", "batch", "compare", "a2a"])("queues %s, refreshes review, submits explicit decisions and retains task continuity", async (view) => {
    let decided = false;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      if (!String(path).startsWith("/a2a/")) return json({ choices: [{ message: { role: "assistant", content: "Model succeeded" } }] });
      const body = JSON.parse(String(options?.body));
      if (body.params.message?.metadata) decided = true;
      return json({ jsonrpc: "2.0", id: body.id, result: body.method === "GetTask" ? task(decided ? "TASK_STATE_COMPLETED" : "TASK_STATE_INPUT_REQUIRED") : { task: task("TASK_STATE_SUBMITTED") } });
    });
    let label = "Agent tool approvals", refresh = "Refresh agent task", scope = screen;
    if (view === "chat" || view === "batch") {
      render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab={view} />);
      await userEvent.click(screen.getByLabelText("Run agent in background")); expect(screen.getByLabelText("Stream agent task")).toBeDisabled();
      await userEvent.type(screen.getByLabelText(view === "chat" ? "Agent prompt" : "Agent batch prompts"), "Background prompt");
      await userEvent.click(screen.getByRole("button", { name: view === "chat" ? "Send to agent" : "Run agent tests" }));
      if (view === "batch") { label = "Test 1 tool approvals"; refresh = "Refresh test 1 task"; }
    } else if (view === "compare") {
      render(<ComparePlayground connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
      const panel = within(screen.getByRole("region", { name: "Comparison 2" }));
      await userEvent.click(panel.getByLabelText("Comparison type 2")); await userEvent.click(screen.getByRole("option", { name: "Saved agent" })); await userEvent.type(panel.getByLabelText("Agent 2"), "writer");
      await userEvent.click(screen.getByLabelText("Run comparison agents in background")); await userEvent.type(screen.getByLabelText("Comparison prompt"), "Background prompt"); await userEvent.click(screen.getByRole("button", { name: "Compare models" }));
      label = "Comparison 2 agent tool approvals"; refresh = "Refresh comparison 2 task"; scope = panel as typeof screen;
    } else {
      render(<EndpointPlayground endpoint="a2a" connection={connection()} models={[]} connectionChanged={false} connectionControls={null} />);
      await userEvent.type(screen.getByLabelText("Agent ID"), "writer"); await userEvent.click(screen.getByLabelText("Run endpoint agent in background")); expect(screen.getByLabelText("Stream endpoint agent task")).toBeDisabled();
      await userEvent.type(screen.getByLabelText("Endpoint input"), "Background prompt"); await userEvent.click(screen.getByRole("button", { name: "Run endpoint request" }));
      label = "Endpoint agent tool approvals"; refresh = "Refresh endpoint task";
    }
    const assertTiming = () => {
      if (view === "compare" || view === "a2a") {
        expect(scope.getByText("Last request latency").nextElementSibling).toHaveTextContent(/^\d+ ms$/);
        expect(scope.getByText("Agent execution time").nextElementSibling).toHaveTextContent("Not reported");
        expect(scope.queryByText("Latency", { exact: true })).not.toBeInTheDocument();
      } else {
        expect(scope.getByText(/Total agent execution time is not reported/)).toBeInTheDocument();
        expect(scope.getAllByText(/last request:/).length).toBeGreaterThan(0);
      }
    };
    const refreshButton = await screen.findByRole("button", { name: refresh });
    assertTiming();
    expect(scope.getAllByText(/TASK_STATE_SUBMITTED/).length).toBeGreaterThan(0); expect(screen.queryByRole("region", { name: label })).not.toBeInTheDocument();
    expect(mock.mock.calls.filter(([path]) => String(path).startsWith("/a2a/"))).toHaveLength(1);
    await userEvent.click(refreshButton);
    const review = within(await screen.findByRole("region", { name: label }));
    await userEvent.click(review.getByLabelText(`${label} decision 1`)); await userEvent.click(screen.getByRole("option", { name: "Decline" }));
    expect(mock.mock.calls.filter(([path]) => String(path).startsWith("/a2a/"))).toHaveLength(2);
    await userEvent.click(review.getByRole("button", { name: `Continue ${label.toLowerCase()}` }));
    await scope.findAllByText(/TASK_STATE_SUBMITTED/); expect(screen.queryByText("Background final answer")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: refresh })); await screen.findByText("Background final answer");
    assertTiming();
    const bodies = mock.mock.calls.filter(([path]) => String(path).startsWith("/a2a/")).map(([, options]) => JSON.parse(String(options?.body)));
    expect(bodies.map((body) => body.method)).toEqual(["SendMessage", "GetTask", "SendMessage", "GetTask"]);
    expect(bodies[0].params.configuration.returnImmediately).toBe(true); expect(bodies[2].params.configuration.returnImmediately).toBe(true);
    expect(bodies[2].params.message).toMatchObject({ taskId: "task-one", contextId: "ctx-one", metadata: { ai_gateway_tool_approval: { choices: [{ call_id: "call", approved: false }] } } });
    expect(JSON.stringify(bodies[2])).not.toContain("arguments");
    if (view === "compare") expect(screen.getByText("Model succeeded")).toBeInTheDocument();
  });
  it("cancels a queued task only through the explicit server task operation", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const body = JSON.parse(String(options?.body)); return json({ jsonrpc: "2.0", id: body.id, result: { task: task(body.method === "CancelTask" ? "TASK_STATE_CANCELED" : "TASK_STATE_SUBMITTED") } });
    });
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="chat" />);
    await userEvent.click(screen.getByLabelText("Run agent in background")); await userEvent.type(screen.getByLabelText("Agent prompt"), "Queue prompt"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel agent task" }));
    expect(await screen.findByRole("status")).toHaveTextContent("TASK_STATE_CANCELED"); expect(mock).toHaveBeenCalledTimes(2); expect(JSON.parse(String(mock.mock.calls[1][1]?.body)).method).toBe("CancelTask");
  });
  it("shows unavailable latency for a batch prompt cancelled before dispatch", async () => {
    const deferred: { id: string; resolve: (response: Response) => void }[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation((_path, options) => new Promise<Response>((resolve) => deferred.push({ id: JSON.parse(String(options?.body)).id, resolve })));
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="batch" />);
    await userEvent.type(screen.getByLabelText("Agent batch prompts"), "First\nSecond\nUndispatched");
    await userEvent.click(screen.getByRole("button", { name: "Run agent tests" }));
    expect(mock).toHaveBeenCalledTimes(2);
    await userEvent.click(screen.getByRole("button", { name: "Stop agent request" }));
    await act(async () => { for (const pending of deferred) pending.resolve(json({ jsonrpc: "2.0", id: pending.id, result: { task: task("TASK_STATE_COMPLETED") } })); });
    expect(await screen.findByText("Test 3 · cancelled · last request: Unavailable")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("3 cancelled"); expect(screen.queryByText("Background final answer")).not.toBeInTheDocument();
    expect(mock).toHaveBeenCalledTimes(2);
  });
  it("exports background mode without credentials and rejects simultaneous background streaming", async () => {
    render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab="connect" />);
    await userEvent.click(screen.getByLabelText("Run agent in background")); await userEvent.click(screen.getByRole("button", { name: "Get connection code" }));
    expect(screen.getByLabelText("Request code")).toHaveTextContent("returnImmediately"); expect(screen.getByLabelText("Request code")).not.toHaveTextContent("test-key");
    expect(() => agentRequest("writer", "Prompt", undefined, [], true, true)).toThrow("cannot stream");
    expect(() => agentApprovalRequest("writer", { id: "task", contextID: "ctx", state: "TASK_STATE_INPUT_REQUIRED" }, [], true, true)).toThrow("cannot stream");
  });
});
