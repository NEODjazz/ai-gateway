import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { AgentExecution } from "./AgentExecution";
import { ComparePlayground } from "./ComparePlayground";
import { EndpointPlayground } from "./EndpointPlayground";
import { AgentToolApprovals } from "./AgentToolApprovals";
import type { AgentProfile, AgentTask } from "./agents";
import { playgroundConnection } from "./requests";

const profile: AgentProfile = { id: "writer", name: "Writer", model: "model", enabled: true, execution_supported: true, instructions_configured: false, tool_policy_id: "safe", allowed_tools: ["lookup"], max_tool_calls: 3, max_iterations: 3 };
const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
const task: AgentTask = { id: "task-one", contextID: "ctx-one", state: "TASK_STATE_INPUT_REQUIRED", approval: { id: "approval-one", calls: [{ id: "call-one", server: "weather", tool: "lookup", arguments: { query: "Paris" } }] } };
function publicTask() { return { id: task.id, contextId: task.contextID, status: { state: task.state, message: { role: "ROLE_AGENT", taskId: task.id, contextId: task.contextID, parts: [{ text: "Review pending tools" }], metadata: { ai_gateway_tool_approval: { approval_id: task.approval!.id, calls: [{ call_id: "call-one", server_id: "weather", tool_name: "lookup", arguments: { query: "Paris" } }] } } } } }; }
function reply(id: string, result: unknown) { return new Response(JSON.stringify({ jsonrpc: "2.0", id, result }), { headers: { "Content-Type": "application/json" } }); }
async function decide(label: string, value: "Approve" | "Decline") { const control = screen.getByLabelText(label); await userEvent.click(control); const popup = document.getElementById(control.getAttribute("aria-controls")!); if (!popup) throw new Error("Decision popup did not open"); await userEvent.click(within(popup).getByRole("option", { name: value })); }

describe("durable agent approvals", () => {
  it("resolves Compare approvals without resending the successful model panel", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      const request = JSON.parse(String(options?.body));
      if (!String(path).startsWith("/a2a/")) return new Response(JSON.stringify({ choices: [{ message: { role: "assistant", content: "Model panel answer" } }] }), { headers: { "Content-Type": "application/json" } });
      return reply(request.id, { task: request.params.message.metadata ? { id: task.id, contextId: task.contextID, status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Reviewed comparison answer" }] }] } : publicTask() });
    });
    render(<ComparePlayground connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
    const second = within(screen.getByRole("region", { name: "Comparison 2" }));
    await userEvent.click(second.getByLabelText("Comparison type 2")); await userEvent.click(screen.getByRole("option", { name: "Saved agent" })); await userEvent.type(second.getByLabelText("Agent 2"), "writer");
    await userEvent.type(screen.getByLabelText("Comparison prompt"), "Shared"); await userEvent.click(screen.getByRole("button", { name: "Compare models" }));
    await screen.findByText("Model panel answer"); await screen.findByRole("region", { name: "Comparison 2 agent tool approvals" }); expect(screen.getByLabelText("Comparison prompt")).toBeDisabled();
    await decide("Comparison 2 agent tool approvals decision 1", "Approve"); await userEvent.click(screen.getByRole("button", { name: "Continue comparison 2 agent tool approvals" }));
    expect(await screen.findByText("Reviewed comparison answer")).toBeInTheDocument(); expect(screen.getByLabelText("Comparison prompt")).toBeEnabled(); expect(mock.mock.calls.filter(([path]) => path === "/v1/chat/completions")).toHaveLength(1); expect(screen.getAllByText("Shared")).toHaveLength(2);
  });
  it("resolves dedicated A2A approvals and excludes challenges from code export", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const request = JSON.parse(String(options?.body));
      return reply(request.id, { task: request.params.message.metadata ? { id: task.id, contextId: task.contextID, status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Reviewed endpoint answer" }] }] } : publicTask() });
    });
    render(<EndpointPlayground endpoint="a2a" connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
    await userEvent.type(screen.getByLabelText("Agent ID"), "writer"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Question"); await userEvent.click(screen.getByRole("button", { name: "Run endpoint request" }));
    await screen.findByRole("region", { name: "Endpoint agent tool approvals" }); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" })); const code = await screen.findByLabelText("Request code"); expect(code).toHaveTextContent("GetTask"); expect(code).not.toHaveTextContent("approval-one"); await userEvent.keyboard("{Escape}");
    await decide("Endpoint agent tool approvals decision 1", "Decline"); await userEvent.click(screen.getByRole("button", { name: "Continue endpoint agent tool approvals" }));
    expect(await screen.findByText("Reviewed endpoint answer")).toBeInTheDocument(); expect(screen.getByLabelText("Endpoint input")).toBeEnabled(); expect(screen.getAllByText("Question")).toHaveLength(1); expect(mock).toHaveBeenCalledTimes(2);
  });
  it.each(["Approve", "Decline"] as const)("reviews arguments and sends %s only on explicit continuation", async (decision) => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => { const request = JSON.parse(String(options?.body)); return request.params.message.metadata ? reply(request.id, { task: { id: task.id, contextId: task.contextID, status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Finished after decision" }] }] } }) : reply(request.id, { task: publicTask() }); });
    render(<AgentExecution profile={profile} connection={connection()} active disabled={false} tab="chat" />);
    await userEvent.type(screen.getByLabelText("Agent prompt"), "Check weather"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" }));
    const region = await screen.findByRole("region", { name: "Agent tool approvals" }); expect(region).toHaveTextContent("Paris"); expect(screen.getByLabelText("Agent prompt")).toBeDisabled();
    const button = screen.getByRole("button", { name: "Continue agent tool approvals" }); expect(button).toBeDisabled();
    await decide("Agent tool approvals decision 1", decision); expect(mock).toHaveBeenCalledTimes(1); await userEvent.click(button);
    expect(await screen.findByText("Finished after decision")).toBeInTheDocument(); expect(screen.getByLabelText("Agent prompt")).toBeEnabled(); expect(screen.getAllByText("Check weather")).toHaveLength(1);
    const request = JSON.parse(String(mock.mock.calls[1][1]?.body)); expect(request.params.message).toMatchObject({ taskId: task.id, contextId: task.contextID, metadata: { ai_gateway_tool_approval: { approval_id: "approval-one", choices: [{ call_id: "call-one", approved: decision === "Approve" }] } } }); expect(String(mock.mock.calls[1][1]?.body)).not.toContain("test-key");
  });
  it("resets review choices when the server issues a new challenge and renders arguments as text", async () => {
    const onContinue = vi.fn(), value = { ...task, approval: { ...task.approval!, calls: [...task.approval!.calls, { id: "call-two", server: "weather", tool: "lookup", arguments: { text: "<script>private()</script>" } }] } };
    const view = render(<AgentToolApprovals task={value} disabled={false} onContinue={onContinue} />);
    await decide("Agent tool approvals decision 1", "Approve"); expect(screen.getByRole("button", { name: "Continue agent tool approvals" })).toBeDisabled(); await decide("Agent tool approvals decision 2", "Decline");
    await userEvent.click(screen.getByRole("button", { name: "Continue agent tool approvals" })); expect(onContinue).toHaveBeenCalledWith([{ call_id: "call-one", approved: true }, { call_id: "call-two", approved: false }]); expect(document.querySelector("script")).toBeNull();
    view.rerender(<AgentToolApprovals task={{ ...value, approval: { ...value.approval, id: "approval-two" } }} disabled={false} onContinue={onContinue} />); expect(screen.getByRole("button", { name: "Continue agent tool approvals" })).toBeDisabled();
  });
  it("keeps approval errors visible and discards late decisions after credentials change", async () => {
    let complete!: (value: Response) => void, id = "";
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => { const request = JSON.parse(String(options?.body)); return reply(request.id, { task: publicTask() }); });
    const view = render(<AgentExecution profile={profile} connection={connection()} active disabled={false} tab="chat" />); await userEvent.type(screen.getByLabelText("Agent prompt"), "Check"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" })); await screen.findByRole("region", { name: "Agent tool approvals" }); await decide("Agent tool approvals decision 1", "Approve");
    mock.mockImplementationOnce(async () => new Response('{"error":{"message":"Task changed concurrently"}}', { status: 409 })); await userEvent.click(screen.getByRole("button", { name: "Continue agent tool approvals" })); expect(await screen.findByRole("alert")).toHaveTextContent("Task changed concurrently"); expect(screen.getByRole("region", { name: "Agent tool approvals" })).toBeInTheDocument();
    mock.mockImplementationOnce((_path, options) => { id = JSON.parse(String(options?.body)).id; return new Promise((resolve) => complete = resolve); }); await userEvent.click(screen.getByRole("button", { name: "Continue agent tool approvals" }));
    view.rerender(<AgentExecution profile={profile} connection={connection()} active disabled={false} tab="chat" />); await act(async () => complete(reply(id, { task: { id: task.id, contextId: task.contextID, status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Late old result" }] }] } }))); expect(screen.queryByText("Late old result")).not.toBeInTheDocument(); expect(screen.queryByRole("region", { name: "Agent tool approvals" })).not.toBeInTheDocument();
  });
  it("reports pending batch approvals separately and resolves the selected test", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => { const request = JSON.parse(String(options?.body)); return reply(request.id, { task: request.params.message.metadata ? { id: task.id, contextId: task.contextID, status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Reviewed batch result" }] }] } : publicTask() }); });
    render(<AgentExecution profile={profile} connection={connection()} active disabled={false} tab="batch" />); await userEvent.type(screen.getByLabelText("Agent batch prompts"), "Check"); await userEvent.click(screen.getByRole("button", { name: "Run agent tests" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("0 completed · 1 pending · 0 failed")); expect(mock).toHaveBeenCalledTimes(1); await decide("Test 1 tool approvals decision 1", "Decline"); await userEvent.click(screen.getByRole("button", { name: "Continue test 1 tool approvals" })); expect(await screen.findByText("Reviewed batch result")).toBeInTheDocument(); expect(screen.getByRole("status")).toHaveTextContent("1 completed · 0 pending"); expect(within(screen.getByRole("region", { name: "Saved agent execution" })).queryByRole("alert")).not.toBeInTheDocument();
  });
});
