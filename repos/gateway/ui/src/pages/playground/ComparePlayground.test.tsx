import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { ComparePlayground } from "./ComparePlayground";
import { playgroundConnection } from "./requests";

const connection = () => playgroundConnection(new APIClient(() => "test-key"), "session", "", "");
function setup() {
  return render(<ComparePlayground connection={connection()} models={["alpha", "beta", "gamma"]} connectionChanged={false} connectionControls={null} />);
}
function answer(text: string) { return new Response(JSON.stringify({ choices: [{ message: { role: "assistant", content: text, reasoning_content: "retained reasoning" } }], usage: { prompt_tokens: 0, completion_tokens: 2 } }), { headers: { "Content-Type": "application/json" } }); }
async function send(text: string) { await userEvent.type(screen.getByLabelText("Comparison prompt"), text); await userEvent.click(screen.getByRole("button", { name: "Compare models" })); }

describe("Compare Playground", () => {
  it("keeps independently selected manual models and histories when catalog options change", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => answer(`Reply for ${JSON.parse(String(options?.body)).model}`));
    const value = connection(); const view = render(<ComparePlayground connection={value} models={["alpha", "beta"]} connectionChanged={false} connectionControls={null} />);
    await userEvent.click(screen.getByRole("button", { name: "Model 1: enter ID manually" })); fireEvent.change(screen.getByLabelText("Model 1"), { target: { value: "manual-model" } });
    await send("First prompt"); await screen.findByText("Reply for manual-model"); await screen.findByText("Reply for beta");
    view.rerender(<ComparePlayground connection={value} models={["gamma"]} connectionChanged={false} connectionControls={null} />);
    expect(screen.getByLabelText("Model 1")).toHaveValue("manual-model"); expect(screen.getByLabelText("Model 2")).toHaveValue("beta");
    await send("Second prompt"); await waitFor(() => expect(mock).toHaveBeenCalledTimes(4));
    const bodies = mock.mock.calls.map(([, options]) => JSON.parse(String(options?.body)));
    expect(bodies.map((body) => body.model)).toEqual(["manual-model", "beta", "manual-model", "beta"]);
    expect(bodies[2].messages).toEqual([{ role: "user", content: "First prompt" }, { role: "assistant", content: "Reply for manual-model", reasoning_content: "retained reasoning" }, { role: "user", content: "Second prompt" }]);
  });

  it("retains actual output and usage when a tool batch is invalid and requires clearing that panel", async () => {
    const call = { id: "duplicate", type: "function", function: { name: "lookup", arguments: "{}" } };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => JSON.parse(String(options?.body)).model === "alpha"
      ? new Response(JSON.stringify({ choices: [{ message: { role: "assistant", content: "Actual alpha output", tool_calls: [call, call] } }], usage: { prompt_tokens: 9, completion_tokens: 3 } }), { headers: { "Content-Type": "application/json" } }) : answer("Other panel succeeded"));
    setup(); await send("Review invalid calls"); await screen.findByText("Other panel succeeded");
    expect(await screen.findByText("Actual alpha output")).toBeInTheDocument();
    const first = within(screen.getByRole("region", { name: "Comparison 1" }));
    expect(first.getByRole("alert")).toHaveTextContent("Tool review failed");
    expect(first.getByText("Input tokens").nextElementSibling).toHaveTextContent("9");
    expect(first.getByText("Output tokens").nextElementSibling).toHaveTextContent("3");
    expect(screen.getByLabelText("Comparison prompt")).toBeDisabled(); expect(mock).toHaveBeenCalledTimes(2);
    await userEvent.click(first.getByRole("button", { name: "Clear comparison 1 chat" }));
    expect(screen.getByLabelText("Comparison prompt")).toBeEnabled(); expect(screen.getByText("Other panel succeeded")).toBeInTheDocument();
  });
  it("reviews declared function results per panel without MCP execution and binds the original definition", async () => {
    const tools = [{ type: "function", function: { name: "query", parameters: { type: "object", properties: { id: { type: "integer" } }, required: ["id"] } } }];
    const rawArguments = '{"id":9007199254740993}';
    const output = 'rows: 9007199254740993\n<not-json>';
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    let runs = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path, options) => {
      if (path !== "/v1/chat/completions") throw new Error("Unexpected tool execution");
      if (JSON.parse(String(options?.body)).model === "beta") return answer("Independent beta output");
      return ++runs === 1 ? json({ choices: [{ message: { content: null, tool_calls: [{ id: "call_manual", type: "function", function: { name: "query", arguments: rawArguments } }] } }], usage: { prompt_tokens: 8, completion_tokens: 3 } })
        : runs === 2 ? json({ error: { message: "Manual continuation unavailable" } }, 503) : answer("Function result received");
    });
    setup(); const first = within(screen.getByRole("region", { name: "Comparison 1" }));
    await userEvent.click(first.getByText("Model settings"));
    fireEvent.change(first.getByLabelText("Advanced parameters 1"), { target: { value: JSON.stringify({ tools }) } });
    await send("Review functions"); await screen.findByText("Independent beta output");
    const approvals = within(await screen.findByRole("region", { name: "Comparison 1 tool approvals" }));
    expect(approvals.getByText(rawArguments)).toBeInTheDocument(); expect(approvals.queryByRole("button", { name: "Execute query" })).not.toBeInTheDocument();
    const next = approvals.getByRole("button", { name: "Continue comparison 1 with tool results" });
    expect(next).toBeDisabled();
    fireEvent.change(approvals.getByLabelText("Function tool result call_manual"), { target: { value: output } }); expect(next).toBeDisabled();
    await userEvent.click(approvals.getByRole("button", { name: "Use result for query" })); expect(next).toBeEnabled(); expect(runs).toBe(1);
    fireEvent.change(first.getByLabelText("Advanced parameters 1"), { target: { value: JSON.stringify({ tools: [] }) } });
    await userEvent.click(next); expect(await first.findByRole("alert")).toHaveTextContent("function tool definition changed"); expect(runs).toBe(1);
    fireEvent.change(first.getByLabelText("Advanced parameters 1"), { target: { value: JSON.stringify({ tools }) } });
    await userEvent.click(next); expect(await first.findByRole("alert")).toHaveTextContent("Manual continuation unavailable");
    expect(approvals.getByLabelText("Function tool result call_manual")).toHaveValue(output); expect(screen.getByLabelText("Comparison prompt")).toBeDisabled();
    await userEvent.click(next); await screen.findByText("Function result received"); expect(screen.getByLabelText("Comparison prompt")).toBeEnabled();
    const bodies = mock.mock.calls.filter(([, options]) => JSON.parse(String(options?.body)).model === "alpha").map(([, options]) => JSON.parse(String(options?.body)));
    expect(bodies).toHaveLength(3); expect(bodies[1]).toEqual(bodies[2]); expect(bodies[2].tools).toEqual(tools);
    expect(bodies[2].messages).toEqual([{ role: "user", content: "Review functions" }, { role: "assistant", content: "", tool_calls: [{ id: "call_manual", type: "function", function: { name: "query", arguments: rawArguments } }] }, { role: "tool", tool_call_id: "call_manual", content: output }]);
    expect(mock.mock.calls.filter(([, options]) => JSON.parse(String(options?.body)).model === "beta")).toHaveLength(1);
  });
  it("requires per-panel tool approval, reuses retry identity and continues without a phantom user", async () => {
    let alphaRuns = 0, executions = 0;
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      if (String(url).endsWith("/tools")) return json({ tools: [{ name: "lookup", inputSchema: { type: "object" } }] });
      if (String(url).endsWith("/tools/lookup")) { executions++; return executions === 1 ? json({ error: { message: "MCP temporarily offline" } }, 503) : json({ content: [{ type: "text", text: "Tool output" }] }); }
      const body = JSON.parse(String(options?.body));
      if (body.model !== "alpha") return answer("Beta answer");
      return ++alphaRuns === 1 ? json({ choices: [{ message: { content: null, tool_calls: [{ id: "call-alpha", type: "function", function: { name: "lookup", arguments: '{"query":"demo"}' } }] } }] }) : answer("Final alpha answer");
    });
    setup(); const first = within(screen.getByRole("region", { name: "Comparison 1" }));
    await userEvent.click(first.getByText("Tools, resources and policies"));
    await userEvent.type(first.getByLabelText("Tool discovery server"), "weather"); await userEvent.click(first.getByRole("button", { name: "Load MCP tools" }));
    await userEvent.click(await first.findByLabelText("MCP function lookup")); await send("Same prompt");
    await screen.findByText("Beta answer"); const approvals = within(await screen.findByRole("region", { name: "Comparison 1 tool approvals" }));
    expect(executions).toBe(0); expect(screen.getByLabelText("Comparison prompt")).toBeDisabled();
    expect(approvals.getByRole("button", { name: "Continue comparison 1 with tool results" })).toBeDisabled();
    await userEvent.click(approvals.getByRole("button", { name: "Execute lookup" })); expect(await approvals.findByRole("alert")).toHaveTextContent("MCP temporarily offline");
    await userEvent.click(approvals.getByRole("button", { name: "Retry lookup" })); await userEvent.click(approvals.getByRole("button", { name: "Continue comparison 1 with tool results" }));
    expect(await screen.findByText("Final alpha answer")).toBeInTheDocument(); expect(screen.getByLabelText("Comparison prompt")).toBeEnabled();
    const toolRequests = mock.mock.calls.filter(([url]) => String(url).endsWith("/tools/lookup"));
    expect(new Headers(toolRequests[0][1]?.headers).get("Idempotency-Key")).toBe(new Headers(toolRequests[1][1]?.headers).get("Idempotency-Key"));
    const continued = mock.mock.calls.filter(([url]) => url === "/v1/chat/completions").map(([, options]) => JSON.parse(String(options?.body))).find((body) => body.messages.at(-1).role === "tool");
    expect(continued.messages.map((turn: { role: string }) => turn.role)).toEqual(["user", "assistant", "tool"]); expect(continued.messages.at(-1).tool_call_id).toBe("call-alpha");
    expect(mock.mock.calls.filter(([, options]) => String(options?.body).includes('"model":"beta"'))).toHaveLength(1);
  });
  it("allows only decline for unbound functions and keeps a failed continuation recoverable", async () => {
    let count = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, options) => {
      if (JSON.parse(String(options?.body)).model === "beta") return answer("Other model answer");
      count++;
      return count === 1 ? new Response(JSON.stringify({ choices: [{ message: { tool_calls: [{ id: "call-one", type: "function", function: { name: "unknown", arguments: "{}" } }] } }] }), { headers: { "Content-Type": "application/json" } }) : count === 2 ? new Response('{"error":{"message":"Continuation offline"}}', { status: 503 }) : answer("Recovered answer");
    });
    setup(); await send("Review"); const approvals = within(await screen.findByRole("region", { name: "Comparison 1 tool approvals" }));
    expect(approvals.getByRole("button", { name: "Execute unknown" })).toBeDisabled();
    await userEvent.click(approvals.getByRole("button", { name: "Decline unknown" })); await userEvent.click(approvals.getByRole("button", { name: "Continue comparison 1 with tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Continuation offline"); expect(screen.getByLabelText("Comparison prompt")).toBeDisabled();
    await userEvent.click(approvals.getByRole("button", { name: "Continue comparison 1 with tool results" })); await screen.findByText("Recovered answer");
    expect(mock.mock.calls).toHaveLength(4); expect(screen.getByLabelText("Comparison prompt")).toBeEnabled();
  });
  it("compares a model and saved agent with shared attachments and independent continuation", async () => {
    const catalog = { mcp_servers: [], mcp_toolsets: [], policies: [], tags: [], agents: [{ id: "writer", name: "Writer", model: "alpha", execution_supported: true }], truncated: false };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      if (String(url).includes("catalog")) return new Response(JSON.stringify(catalog));
      const body = JSON.parse(String(options?.body));
      if (String(url).startsWith("/a2a/")) return new Response(JSON.stringify({ jsonrpc: "2.0", id: body.id, result: { task: { id: "task-writer", contextId: "ctx-writer", status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: "Agent answer" }] }] } } }), { headers: { "Content-Type": "application/json" } });
      return answer("Model answer");
    });
    setup(); await userEvent.click(screen.getByRole("button", { name: "Load authorized agents" }));
    const second = within(screen.getByRole("region", { name: "Comparison 2" }));
    await userEvent.click(second.getByLabelText("Comparison type 2")); await userEvent.click(screen.getByRole("option", { name: "Saved agent" }));
    await userEvent.click(second.getByLabelText("Agent 2")); await userEvent.click(screen.getByRole("option", { name: "Writer · alpha" }));
    expect(second.queryByText("Model settings")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Comparison attachments"), { target: { files: [new File(["pdf"], "brief.pdf", { type: "application/pdf" })] } });
    await screen.findByText("brief.pdf"); await send("Shared prompt");
    expect(await screen.findByText("Agent answer")).toBeInTheDocument(); await screen.findByText("Model answer");
    expect(second.getAllByText("Not reported")).toHaveLength(2);
    const first = mock.mock.calls.find(([url]) => url === "/a2a/writer");
    expect(new Headers(first?.[1]?.headers).get("A2A-Version")).toBe("1.0");
    expect(JSON.parse(String(first?.[1]?.body)).params.message.parts).toEqual([{ text: "Shared prompt" }, { raw: "cGRm", filename: "brief.pdf", mediaType: "application/pdf" }]);
    await send("Follow up"); await waitFor(() => expect(mock.mock.calls.filter(([url]) => url === "/a2a/writer")).toHaveLength(2));
    const continued = mock.mock.calls.filter(([url]) => url === "/a2a/writer")[1];
    expect(JSON.parse(String(continued[1]?.body)).params.message).toMatchObject({ taskId: "task-writer", contextId: "ctx-writer", parts: [{ text: "Follow up" }] });
  });
  it("blocks shared prompts for pending agent tasks and resumes after verified task refresh", async () => {
    let taskState = "TASK_STATE_WORKING";
    vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
      const body = JSON.parse(String(options?.body));
      if (!String(url).startsWith("/a2a/")) return answer("Model succeeded");
      const task = { id: "task-one", contextId: "ctx-one", status: { state: taskState }, artifacts: taskState === "TASK_STATE_COMPLETED" ? [{ parts: [{ text: "Task complete" }] }] : [] };
      return new Response(JSON.stringify({ jsonrpc: "2.0", id: body.id, result: body.method === "GetTask" ? task : { task } }), { headers: { "Content-Type": "application/json" } });
    });
    setup(); const second = within(screen.getByRole("region", { name: "Comparison 2" }));
    await userEvent.click(second.getByLabelText("Comparison type 2")); await userEvent.click(screen.getByRole("option", { name: "Saved agent" }));
    await userEvent.type(second.getByLabelText("Agent 2"), "writer"); await send("Start");
    await screen.findByText("Model succeeded"); expect(screen.getByLabelText("Comparison prompt")).toBeDisabled(); expect(second.getByRole("button", { name: "Cancel comparison 2 task" })).toBeEnabled();
    taskState = "TASK_STATE_COMPLETED"; await userEvent.click(second.getByRole("button", { name: "Refresh comparison 2 task" }));
    await screen.findByText("Task complete"); expect(screen.getByLabelText("Comparison prompt")).toBeEnabled();
  });
  it("runs panels concurrently with isolated sessions and preserves typed histories", async () => {
    const deferred: ((response: Response) => void)[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((resolve) => deferred.push(resolve)));
    setup(); await send("First prompt");
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
    const bodies = mock.mock.calls.map(([, options]) => JSON.parse(String(options?.body)));
    expect(bodies.map((body) => body.model)).toEqual(["alpha", "beta"]);
    const sessions = mock.mock.calls.map(([, options]) => new Headers(options?.headers).get("X-Session-ID"));
    expect(new Set(sessions).size).toBe(2);
    await act(async () => { deferred[0](answer("Alpha answer")); deferred[1](answer("Beta answer")); });
    expect(await screen.findByText("Alpha answer")).toBeInTheDocument();
    expect(screen.getByText("Beta answer")).toBeInTheDocument();
    await send("Follow up"); await waitFor(() => expect(mock).toHaveBeenCalledTimes(4));
    const next = mock.mock.calls.slice(2).map(([, options]) => JSON.parse(String(options?.body)));
    expect(next[0].messages).toEqual([{ role: "user", content: "First prompt" }, { role: "assistant", content: "Alpha answer", reasoning_content: "retained reasoning" }, { role: "user", content: "Follow up" }]);
    expect(next[1].messages[1].content).toBe("Beta answer");
    await act(async () => { deferred[2](answer("Next alpha")); deferred[3](answer("Next beta")); });
  });
  it("keeps a successful panel visible when the other fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => JSON.parse(String(options?.body)).model === "alpha" ? answer("Successful") : new Response('{"error":{"message":"Provider offline"}}', { status: 503 }));
    setup(); await send("Test failure");
    expect(await screen.findByText("Successful")).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent("Provider offline");
    expect(within(screen.getByRole("region", { name: "Comparison 1" })).getByText("0")).toBeInTheDocument();
  });
  it("synchronizes settings, permits independent settings and validates all panels before transport", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => answer("Answer"));
    setup();
    await userEvent.click(within(screen.getByRole("region", { name: "Comparison 1" })).getByText("Model settings"));
    await userEvent.click(within(screen.getByRole("region", { name: "Comparison 2" })).getByText("Model settings"));
    await userEvent.type(screen.getByLabelText("Temperature 1"), "0.4");
    expect(screen.getByLabelText("Temperature 2")).toHaveValue(0.4);
    await userEvent.click(screen.getByLabelText("Sync settings across models"));
    await userEvent.clear(screen.getByLabelText("Max tokens 2")); await userEvent.type(screen.getByLabelText("Max tokens 2"), "0");
    await send("Validate"); expect(await screen.findByRole("alert")).toHaveTextContent("Maximum output tokens"); expect(mock).not.toHaveBeenCalled();
    await userEvent.clear(screen.getByLabelText("Max tokens 2")); await userEvent.type(screen.getByLabelText("Max tokens 2"), "100");
    await userEvent.click(screen.getByRole("button", { name: "Compare models" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
    expect(mock.mock.calls.map(([, options]) => JSON.parse(String(options?.body)).max_completion_tokens)).toEqual([256, 100]);
  });
  it("bounds panels at three and retains at least one", async () => {
    setup(); await userEvent.click(screen.getByRole("button", { name: "Add comparison" }));
    expect(screen.getByRole("button", { name: "Add comparison" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Remove comparison 3" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove comparison 2" }));
    expect(screen.getByRole("button", { name: "Remove comparison 1" })).toBeDisabled();
  });
  it("cancels every request and discards late answers from a transport ignoring abort", async () => {
    const deferred: ((response: Response) => void)[] = [];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((resolve) => deferred.push(resolve)));
    setup(); await send("Cancel"); await userEvent.click(screen.getByRole("button", { name: "Stop comparison" }));
    expect(mock.mock.calls.every(([, options]) => options?.signal?.aborted)).toBe(true);
    await act(async () => { deferred[0](answer("Late alpha")); deferred[1](answer("Late beta")); });
    expect(screen.queryByText("Late alpha")).not.toBeInTheDocument(); expect(screen.queryByText("Late beta")).not.toBeInTheDocument();
    expect(screen.getAllByRole("alert")).toHaveLength(2);
    expect(screen.getByLabelText("Comparison prompt")).toHaveValue("Cancel");
  });
  it("resets conversations when the credential changes without exposing late responses", async () => {
    const deferred: ((response: Response) => void)[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((resolve) => deferred.push(resolve)));
    const view = setup(); await send("Old scope");
    view.rerender(<ComparePlayground connection={connection()} models={["gamma"]} connectionChanged={false} connectionControls={null} />);
    await act(async () => { deferred[0](answer("Previous scope")); deferred[1](answer("Previous scope")); });
    expect(screen.queryByText("Previous scope")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Model 1")).toHaveTextContent("gamma");
  });
  it("shares attachments between panels, retains them in typed history and clears the file field", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => answer("Attachment answer"));
    setup(); const field = screen.getByLabelText("Comparison attachments") as HTMLInputElement;
    const pdf = new File(["pdf"], "brief.pdf", { type: "application/pdf" });
    fireEvent.change(field, { target: { files: [pdf] } });
    await screen.findByText("brief.pdf");
    await userEvent.click(screen.getByRole("button", { name: "Compare models" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Compare models" })).toBeDisabled());
    expect(await screen.findAllByText("Attachment answer")).toHaveLength(2);
    const first = mock.mock.calls.map(([, options]) => JSON.parse(String(options?.body)));
    expect(first[0].messages[0].content).toEqual(first[1].messages[0].content);
    expect(first[0].messages[0].content[1]).toMatchObject({ type: "input_file", filename: "brief.pdf", file_data: "data:application/pdf;base64,cGRm" });
    expect(field.value).toBe("");
    await send("Follow up"); await waitFor(() => expect(mock).toHaveBeenCalledTimes(4));
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body)).messages[0].content).toEqual(first[0].messages[0].content);
  });
  it("checks prompt policies independently and retains success when another panel is blocked", async () => {
    const catalog = { mcp_servers: [], mcp_toolsets: [], agents: [], tags: [], policies: ["strict"], truncated: false };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => String(path).includes("catalog") ? new Response(JSON.stringify(catalog)) : String(path).includes("apply_guardrail") ? new Response('{"allowed":false}') : answer("Allowed panel"));
    setup(); const panel = within(screen.getByRole("region", { name: "Comparison 1" }));
    await userEvent.click(panel.getByText("Tools, resources and policies"));
    await userEvent.click(panel.getByRole("button", { name: "Load resource catalog" }));
    await userEvent.click(await panel.findByLabelText("Prompt policy strict"));
    await send("Policy check");
    expect(await panel.findByRole("alert")).toHaveTextContent("blocked by policy strict");
    expect(await screen.findByText("Allowed panel")).toBeInTheDocument();
    const inference = mock.mock.calls.filter(([path]) => path === "/v1/chat/completions");
    expect(inference).toHaveLength(1);
    expect(JSON.parse(String(inference[0][1]?.body)).model).toBe("beta");
  });
  it("exports quoted CSV with formula protection and revokes the download URL", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => answer('=SUM(1,2)\n"quoted"'));
    const create = vi.fn((_blob: Blob) => "blob:test"), revoke = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    setup(); await send("=test");
    await waitFor(() => expect(screen.getByRole("button", { name: "Export results" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Export results" }));
    const blob = create.mock.calls[0][0] as Blob;
    const csv = await new Promise<string>((resolve) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.readAsText(blob); });
    expect(csv).toContain('"\'=test"'); expect(csv).toContain('"\'=SUM(1,2)\n""quoted"""');
    expect(revoke).toHaveBeenCalledWith("blob:test");
    Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL");
  });
});
