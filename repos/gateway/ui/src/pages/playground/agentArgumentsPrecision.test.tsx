import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { AgentExecution } from "./AgentExecution";
import { ComparePlayground } from "./ComparePlayground";
import { EndpointPlayground } from "./EndpointPlayground";
import { AgentToolApprovals } from "./AgentToolApprovals";
import { agentApprovalRequest, agentRequest, agentTaskRequest, runAgentRequest, type AgentRun } from "./agents";
import { exactObjectText, parseAgentJSON, parseNativeJSON, stringifyExactJSON } from "./exactJSON";
import { playgroundConnection } from "./requests";

const argumentsJSON = '{ "id":9007199254740993,"amount":0.1234567890123456789012345,"nested":[{"negative":-9007199254740993,"exponent":1.2300e+24}],"text":"literal \\u007d and <script>ignored</script>" }';
const statusJSON = `{"state":"TASK_STATE_INPUT_REQUIRED","message":{"role":"ROLE_AGENT","taskId":"task-one","contextId":"ctx-one","parts":[{"text":"Review lookup"}],"metadata":{"ai_gateway_tool_approval":{"approval_id":"approval-one","calls":[{"call_id":"call-one","server_id":"server","tool_name":"lookup","arguments":${argumentsJSON}}]}}}}`;
const taskJSON = `{"id":"task-one","contextId":"ctx-one","status":${statusJSON}}`;
const envelope = (id: string, resultJSON: string) => `{"jsonrpc":"2.0","id":${JSON.stringify(id)},"result":${resultJSON}}`;
const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");

function wire(id: string, transport: string) {
  if (transport !== "stream") return new Response(envelope(id, transport === "refresh" ? taskJSON : `{"task":${taskJSON}}`), { headers: { "Content-Type": "application/json" } });
  return new Response(`data: ${envelope(id, '{"task":{"id":"task-one","contextId":"ctx-one","status":{"state":"TASK_STATE_WORKING"}}}')}\n\ndata: ${envelope(id, `{"statusUpdate":{"taskId":"task-one","contextId":"ctx-one","status":${statusJSON}}}`)}\n\n`, { headers: { "Content-Type": "text/event-stream" } });
}

describe("agent tool review argument precision", () => {
  it.each(["json", "fallback", "stream", "refresh"])("keeps original numeric literals visible over %s without rewriting execution arguments", async (transport) => {
    const request = transport === "refresh" ? agentTaskRequest("writer", { id: "task-one", contextID: "ctx-one", state: "TASK_STATE_INPUT_REQUIRED" }, "GetTask") : agentRequest("writer", "Review lookup", undefined, [], transport !== "json");
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(wire(request.body.id, transport)), observed: AgentRun[] = [];
    const result = await runAgentRequest(connection(), request, new AbortController().signal, (value) => observed.push(value));
    const onContinue = vi.fn(); render(<AgentToolApprovals task={result.task!} disabled={false} onContinue={onContinue} />);
    const review = within(screen.getByRole("region", { name: "Agent tool approvals" })), output = review.getByRole("article").querySelector("pre")!;
    expect(output.textContent).toContain("9007199254740993"); expect(output.textContent).toContain("0.1234567890123456789012345"); expect(output.textContent).toContain("1.2300e+24"); expect(output.querySelector("script")).toBeNull();
    expect(stringifyExactJSON(result.response)).toContain(`"arguments":${argumentsJSON}`); expect(mock).toHaveBeenCalledOnce();
    if (transport === "stream") expect(stringifyExactJSON(observed.at(-1)!.response)).toContain(argumentsJSON);
    await userEvent.click(review.getByLabelText("Agent tool approvals decision 1")); await userEvent.click(screen.getByRole("option", { name: "Decline" })); expect(onContinue).not.toHaveBeenCalled(); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(review.getByRole("button", { name: "Continue agent tool approvals" })); expect(onContinue).toHaveBeenCalledWith([{ call_id: "call-one", approved: false }]);
    const decision = agentApprovalRequest("writer", result.task!, [{ call_id: "call-one", approved: false }], transport === "stream");
    expect(decision.body.params.message.metadata).toEqual({ ai_gateway_tool_approval: { approval_id: "approval-one", choices: [{ call_id: "call-one", approved: false }] } }); expect(JSON.stringify(decision.body)).not.toContain("arguments");
  });
});


describe("bounded agent argument JSON codec", () => {
  it("preserves nested arguments and duplicate-member last-value semantics without accepting response markers", () => {
    const parsed = parseAgentJSON(`{"calls":[{"call_id":"call","server_id":"server","tool_name":"lookup","arguments":{},"arguments":${argumentsJSON}}]}`) as { calls: { arguments: unknown }[] };
    expect(exactObjectText(parsed.calls[0].arguments)).toBe(argumentsJSON);
    expect(Object.keys(parsed.calls[0].arguments as object)).not.toContain("native tool argument JSON");
    const ordinary = parseAgentJSON(`{"arguments":${argumentsJSON},"native tool argument JSON":"injected","rawArguments":"injected"}`) as { arguments: unknown };
    expect(exactObjectText(ordinary.arguments)).toBe(JSON.stringify(JSON.parse(argumentsJSON)));
    expect(exactObjectText(ordinary.arguments)).not.toBe(argumentsJSON);
    (parsed.calls[0].arguments as Record<string, unknown>).id = 3;
    expect(() => stringifyExactJSON(parsed)).toThrow("arguments changed");
  });
  it("rejects malformed, too deep or oversized responses and retains the smaller native response bound", () => {
    expect(() => parseAgentJSON('{"broken":')).toThrow();
    expect(() => parseAgentJSON(taskJSON + " null")).toThrow();
    expect(() => parseAgentJSON("[".repeat(130) + "0" + "]".repeat(130))).toThrow("128-level depth limit");
    expect(() => parseAgentJSON('"' + "я".repeat(2 * 1024 * 1024 + 1) + '"')).toThrow("4 MiB");
    const withinAgentLimit = '"' + "a".repeat(2 * 1024 * 1024) + '"';
    expect(() => parseAgentJSON(withinAgentLimit)).not.toThrow();
    expect(() => parseNativeJSON(withinAgentLimit)).toThrow("2 MiB");
  });
});


describe("exact agent review in Playground workspaces", () => {
  const profile = { id: "writer", name: "Writer", model: "model", instructions_configured: true, tool_policy_id: "policy", allowed_tools: ["lookup"], max_tool_calls: 2, max_iterations: 2, enabled: true, execution_supported: true };
  it.each(["chat", "batch", "compare", "a2a"].flatMap((view) => ["json", "stream"].map((transport) => ({ view, transport }))))("retains exact arguments in $view over $transport and after Refresh", async ({ view, transport }) => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      if (!String(path).startsWith("/a2a/")) return new Response(JSON.stringify({ choices: [{ message: { role: "assistant", content: "Model succeeded" } }] }), { headers: { "Content-Type": "application/json" } });
      const body = JSON.parse(String(options?.body)); return wire(body.id, body.method === "GetTask" ? "refresh" : transport);
    });
    let label = "Agent tool approvals", refresh = "Refresh agent task";
    if (view === "chat" || view === "batch") {
      render(<AgentExecution connection={connection()} profile={profile} disabled={false} active tab={view} />);
      if (transport === "stream") await userEvent.click(screen.getByLabelText("Stream agent task"));
      await userEvent.type(screen.getByLabelText(view === "chat" ? "Agent prompt" : "Agent batch prompts"), "Review lookup");
      await userEvent.click(screen.getByRole("button", { name: view === "chat" ? "Send to agent" : "Run agent tests" }));
      if (view === "batch") { label = "Test 1 tool approvals"; refresh = "Refresh test 1 task"; }
    } else if (view === "compare") {
      render(<ComparePlayground connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
      const panel = within(screen.getByRole("region", { name: "Comparison 2" }));
      await userEvent.click(panel.getByLabelText("Comparison type 2")); await userEvent.click(screen.getByRole("option", { name: "Saved agent" }));
      await userEvent.type(panel.getByLabelText("Agent 2"), "writer");
      if (transport === "json") await userEvent.click(screen.getByLabelText("Stream comparison"));
      await userEvent.type(screen.getByLabelText("Comparison prompt"), "Review lookup"); await userEvent.click(screen.getByRole("button", { name: "Compare models" }));
      label = "Comparison 2 agent tool approvals"; refresh = "Refresh comparison 2 task";
    } else {
      render(<EndpointPlayground endpoint="a2a" connection={connection()} models={[]} connectionChanged={false} connectionControls={null} />);
      await userEvent.type(screen.getByLabelText("Agent ID"), "writer"); if (transport === "stream") await userEvent.click(screen.getByLabelText("Stream endpoint agent task"));
      await userEvent.type(screen.getByLabelText("Endpoint input"), "Review lookup"); await userEvent.click(screen.getByRole("button", { name: "Run endpoint request" }));
      label = "Endpoint agent tool approvals"; refresh = "Refresh endpoint task";
    }
    const check = async () => {
      const review = await screen.findByRole("region", { name: label });
      expect(review.querySelector("pre")!.textContent).toBe(argumentsJSON);
      expect(review.querySelector("script")).toBeNull();
      if (view === "a2a") expect(screen.getByText("Response details").parentElement!.querySelector("pre")!.textContent).toContain(argumentsJSON);
      if (view === "compare") expect(screen.getByText("Model succeeded")).toBeInTheDocument();
    };
    await check(); await userEvent.click(screen.getByRole("button", { name: refresh })); await check();
    const agentCalls = mock.mock.calls.filter(([path]) => String(path).startsWith("/a2a/"));
    expect(agentCalls).toHaveLength(2); expect(JSON.parse(String(agentCalls[0][1]?.body)).method).toBe(transport === "stream" ? "SendStreamingMessage" : "SendMessage");
    expect(JSON.parse(String(agentCalls[1][1]?.body)).method).toBe("GetTask");
    expect(agentCalls.every(([, options]) => !JSON.parse(String(options?.body)).params.message?.metadata)).toBe(true);
  });
});
