import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "../auth/AuthContext";
import { PlaygroundPage } from "./PlaygroundPage";

function authenticated() {
  sessionStorage.setItem("ai-gateway.admin-token", "playground-token");
  return render(<AuthProvider><PlaygroundPage /></AuthProvider>);
}

function streamResponse(chunks: string[]) {
  const encoder = new TextEncoder();
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    }
  }), { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

afterEach(() => document.querySelectorAll('meta[name="ai-gateway-playground-origins"]').forEach((node) => node.remove()));

describe("PlaygroundPage", () => {
  it.each(["json", "fallback", "stream"])("shows failed Responses output and real usage over %s without retrying", async (transport) => {
    const response = { id: "resp_failed", status: "failed", output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "Retained failed answer" }] }], usage: { input_tokens: 0, output_tokens: 3 }, error: { message: "Provider execution failed" } };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}')
      : transport === "stream" ? streamResponse([`data: ${JSON.stringify({ type: "response.failed", response })}\n\n`])
      : new Response(JSON.stringify(response), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    if (transport === "json") await userEvent.click(screen.getByLabelText("Stream response"));
    await userEvent.type(screen.getByLabelText("Message"), "Billed failed prompt"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Response failed. Provider execution failed"); expect(screen.getByText("Retained failed answer")).toBeInTheDocument();
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0"); expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("3");
    expect(screen.getByRole("region", { name: "Playground conversation" })).toContainElement(screen.getByRole("region", { name: "Failed response output" }));
    expect(screen.getByText("Status").nextElementSibling).toHaveTextContent("failed"); expect(screen.queryByRole("region", { name: "Background response" })).not.toBeInTheDocument();
    expect(mock.mock.calls.filter(([path]) => path === "/v1/responses")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Clear" })); expect(screen.queryByText("Retained failed answer")).not.toBeInTheDocument(); expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
  it("restores the prompt and attachments after a background Response fails without advancing continuity", async () => {
    let creates = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return new Response('{"data":[{"id":"model"}]}');
      if (path === "/v1/responses/resp_background") return new Response('{"id":"resp_background","status":"failed","usage":{"input_tokens":7,"output_tokens":0},"error":{"message":"Background failed"}}');
      return new Response(JSON.stringify(++creates === 1 ? { id: "resp_background", status: "queued" } : { id: "resp_retry", status: "completed", output_text: "Retry succeeded" }), { headers: { "Content-Type": "application/json" } });
    });
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" })); await userEvent.click(screen.getByLabelText("Stream response"));
    fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"background":true}' } });
    await userEvent.upload(screen.getByLabelText("Conversation attachments"), new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], "kept.png", { type: "image/png" }));
    await userEvent.type(screen.getByLabelText("Message"), "Retained background prompt"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    await screen.findByRole("region", { name: "Background response" }); expect(screen.queryByRole("button", { name: "Remove conversation attachments" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); expect(await screen.findByRole("alert")).toHaveTextContent("Background failed");
    expect(screen.getByLabelText("Message")).toHaveValue("Retained background prompt"); expect(screen.getByText("kept.png")).toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("7"); expect(creates).toBe(1);
    await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByText("Retry succeeded");
    const requests = mock.mock.calls.filter(([path]) => path === "/v1/responses"); expect(requests).toHaveLength(2); expect(requests[1][1]?.body).toBe(requests[0][1]?.body); expect(screen.queryByRole("region", { name: "Failed response output" })).not.toBeInTheDocument();
  });
  it("stores API-managed Responses explicitly, preserves browser store:false and downloads cited files only on demand", async () => {
    const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
    const response = { id: "resp_file", store: true, status: "completed", output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "File ready", annotations: [{ type: "container_file_citation", container_id: "cntr_demo", file_id: "cfile_demo", filename: "report.csv" }] }] }] };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return json({ data: [{ id: "model" }] });
      if (path === "/v1/responses") return json(response);
      if (path === "/v1/responses/resp_file/containers/cntr_demo/files/cfile_demo/content") return new Response("csv", { headers: { "Content-Type": "text/csv" } });
      throw new Error("Unexpected request");
    });
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn(() => "blob:file") });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    try {
      authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
      await userEvent.click(screen.getByLabelText("Stream response"));
      await userEvent.click(screen.getByText("Advanced parameters")); fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"store":false}' } });
      await userEvent.type(screen.getByLabelText("Message"), "Make a file"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("API session management requires store: true");
      expect(mock.mock.calls.filter(([path]) => path === "/v1/responses")).toHaveLength(0);
      fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: "" } });
      await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByText("File ready");
      const requests = () => mock.mock.calls.filter(([path]) => path === "/v1/responses").map(([, options]) => JSON.parse(String(options?.body)));
      expect(requests()[0].store).toBe(true); expect(mock.mock.calls).toHaveLength(2);
      await userEvent.click(screen.getByRole("button", { name: "Download report.csv" })); expect(await screen.findByRole("status")).toHaveTextContent("Download started");
      expect(mock.mock.calls[2][0]).toBe("/v1/responses/resp_file/containers/cntr_demo/files/cfile_demo/content");
      await userEvent.click(screen.getByRole("button", { name: "Clear" })); expect(screen.queryByRole("region", { name: "Response files" })).not.toBeInTheDocument();
      await userEvent.click(screen.getByLabelText("Use API session management")); fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"store":false}' } });
      await userEvent.type(screen.getByLabelText("Message"), "Browser mode"); await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByText("File ready");
      expect(requests()[1].store).toBe(false); expect(requests()[1]).not.toHaveProperty("previous_response_id");
    } finally { Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL"); }
  });
  it("preserves actual output and usage but blocks continuation when native MCP provenance is invalid", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : new Response(JSON.stringify({ id: "resp_invalid", status: "completed", output_text: "Provider output retained", output: [{ type: "mcp_approval_request", id: "approval_foreign", name: "search", server_label: "foreign", arguments: "{}" }], usage: { input_tokens: 7, output_tokens: 2 } }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "Original billed prompt"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Tool review failed"); expect(screen.getByRole("alert")).toHaveTextContent("Clear the conversation");
    expect(screen.getByText("Provider output retained")).toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("7");
    expect(screen.getByLabelText("Message")).toBeDisabled(); expect(screen.getByRole("button", { name: "Get code" })).toBeDisabled();
    expect(mock.mock.calls.filter(([path]) => path === "/v1/responses")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Clear" })); expect(screen.getByLabelText("Message")).toBeEnabled(); expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
  it.each(["api", "browser", "background", "api_queued_failed", "browser_queued_failed"])("reviews native MCP approvals in %s Responses without executing before Continue", async (variant) => {
    const queuedFailure = variant.endsWith("_queued_failed"); variant = variant.replace("_queued_failed", "");
    const tools = [{ type: "mcp", server_label: "documents", server_url: "https://mcp.example.test", allowed_tools: ["search", "write"], require_approval: "always" }];
    const approvals = [
      { id: "approval_search", type: "mcp_approval_request", server_label: "documents", name: "search", arguments: '{"id":9007199254740993}' },
      { id: "approval_write", type: "mcp_approval_request", server_label: "documents", name: "write", arguments: '{"text":"Review me"}' }
    ];
    const advanced = JSON.stringify({ tools, ...(variant === "background" || queuedFailure ? { background: true } : {}) });
    let inference = 0;
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return json({ data: [{ id: "model" }] });
      if (path === "/v1/responses/resp_1") return json({ id: "resp_1", status: "completed", output: approvals });
      if (path === "/v1/responses/resp_approval_failed") return json({ id: "resp_approval_failed", status: "failed", usage: { input_tokens: 7, output_tokens: 3 }, error: { message: "Continuation unavailable" } });
      if (path !== "/v1/responses") throw new Error("Unexpected direct tool execution");
      inference++;
      return inference === 1 ? json({ id: "resp_1", status: variant === "background" ? "queued" : "completed", ...(variant === "background" ? {} : { output: approvals }) }) : inference === 2 ? queuedFailure ? json({ id: "resp_approval_failed", status: "queued" }) : json({ error: { message: "Continuation unavailable" } }, 503) : json({ id: "resp_2", status: "completed", output_text: "Reviewed native result" });
    });
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.click(screen.getByLabelText("Stream response"));
    if (variant === "browser") await userEvent.click(screen.getByLabelText("Use API session management"));
    await userEvent.click(screen.getByText("Advanced parameters"));
    fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: advanced } });
    await userEvent.type(screen.getByLabelText("Message"), "Review native actions"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    if (variant === "background") {
      await screen.findByRole("region", { name: "Background response" });
      fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: JSON.stringify({ tools: [{ ...tools[0], server_label: "other" }] }) } });
      await userEvent.click(screen.getByRole("button", { name: "Refresh background response" }));
    }
    expect(await screen.findByRole("region", { name: "Tool approvals" })).toHaveTextContent("9007199254740993");
    expect(screen.getByLabelText("Message")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Approve search" }));
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Decline write" })); expect(inference).toBe(1);
    expect(screen.getByRole("region", { name: "Tool approvals" })).toHaveTextContent("https://mcp.example.test");
    if (variant !== "background") fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: JSON.stringify({ tools: [{ ...tools[0], server_url: "https://changed.example.test" }] }) } });
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("connection changed"); expect(inference).toBe(1);
    fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: advanced } });
    await userEvent.click(screen.getByRole("button", { name: "Get code" }));
    const code = await screen.findByLabelText("Request code"); expect(code).toHaveTextContent("mcp_approval_response"); expect(code).toHaveTextContent("approval_search");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    if (queuedFailure) { await screen.findByRole("region", { name: "Background response" }); expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(inference).toBe(2); await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); }
    expect(await screen.findByRole("alert")).toHaveTextContent("Continuation unavailable"); expect(screen.getByRole("alert")).toHaveTextContent("retrying can repeat execution");
    expect(screen.getByRole("region", { name: "Tool approvals" })).toHaveTextContent("search · approved");
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" })); await screen.findByText("Reviewed native result");
    expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(screen.getAllByText("Review native actions")).toHaveLength(1);
    const bodies = mock.mock.calls.filter(([path]) => path === "/v1/responses").map(([, options]) => JSON.parse(String(options?.body)));
    const decisions = [{ type: "mcp_approval_response", approval_request_id: "approval_search", approve: true }, { type: "mcp_approval_response", approval_request_id: "approval_write", approve: false }];
    expect(bodies).toHaveLength(3); expect(bodies[1]).toEqual(bodies[2]); expect(bodies[2].tools).toEqual(tools);
    expect(bodies[2].input).toEqual(variant === "browser" ? [{ role: "user", content: "Review native actions" }, ...approvals, ...decisions] : decisions);
    expect(bodies[2].previous_response_id).toBe(variant === "browser" ? undefined : "resp_1");
  });
  it.each(["chat", "api", "browser", "background", "api_failed", "browser_failed", "api_queued_failed", "browser_queued_failed"])("requires explicit function results in %s and preserves their original definition and arguments", async (variant) => {
    const queuedFailure = variant.endsWith("_queued_failed"), reportedFailure = variant.endsWith("_failed"); variant = variant.replace("_queued_failed", "").replace("_failed", "");
    const fn = { name: "query", parameters: { type: "object", properties: { id: { type: "integer" } }, required: ["id"] } };
    const tools = [variant === "chat" ? { type: "function", function: fn } : { type: "function", ...fn }];
    const rawArguments = '{"id":9007199254740993}';
    const calls = variant === "chat" ? [{ id: "manual_1", type: "function", function: { name: "query", arguments: rawArguments } }, { id: "manual_2", type: "function", function: { name: "query", arguments: "{}" } }]
      : [{ type: "function_call", call_id: "manual_1", name: "query", arguments: rawArguments }, { type: "function_call", call_id: "manual_2", name: "query", arguments: "{}" }];
    const advanced = JSON.stringify({ tools, ...(variant === "background" || queuedFailure ? { background: true } : {}) });
    const result = 'rows: 9007199254740993\n<not-json>';
    const endpoint = variant === "chat" ? "/v1/chat/completions" : "/v1/responses";
    const first = variant === "chat" ? { choices: [{ message: { role: "assistant", content: null, tool_calls: calls } }] }
      : { id: "resp_manual", status: "completed", output: calls };
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    let runs = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return json({ data: [{ id: "model" }] });
      if (path === "/v1/responses/resp_manual") return json(first);
      if (path === "/v1/responses/resp_failed_continuation") return json({ id: "resp_failed_continuation", status: "failed", output_text: "Partial continuation output", usage: { input_tokens: 7, output_tokens: 3 }, error: { message: "Function continuation unavailable" } });
      if (path !== endpoint) throw new Error("Unexpected automatic function execution");
      return ++runs === 1 ? json(variant === "background" ? { id: "resp_manual", status: "queued" } : first)
        : runs === 2 ? queuedFailure ? json({ id: "resp_failed_continuation", status: "queued" }) : reportedFailure ? json({ id: "resp_failed_continuation", status: "failed", output_text: "Partial continuation output", usage: { input_tokens: 7, output_tokens: 3 }, error: { message: "Function continuation unavailable" } }) : json({ error: { message: "Function continuation unavailable" } }, 503)
          : json(variant === "chat" ? { choices: [{ message: { content: "Function results received" } }] } : { id: "resp_done", status: "completed", output_text: "Function results received" });
    });
    authenticated(); await screen.findByText("1 authorized model");
    if (variant !== "chat") await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.click(screen.getByLabelText("Stream response"));
    if (variant === "browser") await userEvent.click(screen.getByLabelText("Use API session management"));
    await userEvent.click(screen.getByText("Advanced parameters")); fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: advanced } });
    await userEvent.type(screen.getByLabelText("Message"), "Review function query"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    if (variant === "background") {
      await screen.findByRole("region", { name: "Background response" });
      fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"tools":[]}' } });
      await userEvent.click(screen.getByRole("button", { name: "Refresh background response" }));
    }
    expect(await screen.findByRole("region", { name: "Tool approvals" })).toHaveTextContent("9007199254740993");
    expect(screen.getByLabelText("Message")).toBeDisabled(); expect(screen.queryByRole("button", { name: "Execute query" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Function tool result manual_1"), { target: { value: result } });
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    await userEvent.click(screen.getAllByRole("button", { name: "Use result for query" })[0]);
    await userEvent.click(screen.getByRole("button", { name: "Use empty result for query" }));
    expect(screen.getByText("Empty tool result")).toBeInTheDocument(); expect(runs).toBe(1);
    if (variant !== "background") fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"tools":[]}' } });
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("function tool definition changed"); expect(runs).toBe(1);
    fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: advanced } });
    await userEvent.click(screen.getByRole("button", { name: "Get code" }));
    expect(await screen.findByLabelText("Request code")).toHaveTextContent(variant === "chat" ? "tool_call_id" : "function_call_output");
    expect(screen.getByLabelText("Request code")).toHaveTextContent("9007199254740993"); expect(screen.getByLabelText("Request code")).not.toHaveTextContent("playground-token"); await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    if (queuedFailure) { await screen.findByRole("region", { name: "Background response" }); expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(runs).toBe(2); await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); }
    expect(await screen.findByRole("alert")).toHaveTextContent("Function continuation unavailable");
    expect(screen.getByLabelText("Function tool result manual_1")).toHaveValue(result);
    if (reportedFailure) { expect(screen.getByRole("region", { name: "Failed response output" })).toHaveTextContent("Partial continuation output"); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("7"); }
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" })); await screen.findByText("Function results received");
    expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(screen.getAllByText("Review function query")).toHaveLength(1);
    const bodies = mock.mock.calls.filter(([path]) => path === endpoint).map(([, options]) => JSON.parse(String(options?.body)));
    expect(bodies).toHaveLength(3); expect(bodies[1]).toEqual(bodies[2]); expect(bodies[2].tools).toEqual(tools);
    if (variant === "chat") expect(bodies[2].messages).toEqual([{ role: "user", content: "Review function query" }, { role: "assistant", content: "", tool_calls: calls }, { role: "tool", tool_call_id: "manual_1", content: result }, { role: "tool", tool_call_id: "manual_2", content: "" }]);
    else {
      const outputs = [{ type: "function_call_output", call_id: "manual_1", output: result }, { type: "function_call_output", call_id: "manual_2", output: "" }];
      expect(bodies[2].input).toEqual(variant === "browser" ? [{ role: "user", content: "Review function query" }, ...calls, ...outputs] : outputs);
      expect(bodies[2].previous_response_id).toBe(variant === "browser" ? undefined : "resp_manual");
    }
  });
  it.each(["api", "browser", "background", "api_queued_failed", "browser_queued_failed"])("requires explicit custom results in %s Responses, preserving text on retry and binding definitions", async (variant) => {
    const queuedFailure = variant.endsWith("_queued_failed"); variant = variant.replace("_queued_failed", "");
    const tools = [{ type: "custom", name: "query", format: { type: "text" } }];
    const calls = [{ type: "custom_tool_call", id: "output_item", call_id: "custom_1", name: "query", input: 'status:open\nowner:"demo"' }, { type: "custom_tool_call", call_id: "custom_2", name: "query", input: "status:closed" }];
    const advanced = JSON.stringify({ tools, ...(variant === "background" || queuedFailure ? { background: true } : {}) });
    const originalResult = 'rows: 9007199254740993\nquoted: "exact"\n<not-json>';
    let inference = 0;
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return json({ data: [{ id: "model" }] });
      if (path === "/v1/responses/resp_custom") return json({ id: "resp_custom", status: "completed", output: calls, usage: { input_tokens: 8, output_tokens: 4 } });
      if (path === "/v1/responses/resp_custom_failed") return json({ id: "resp_custom_failed", status: "failed", usage: { input_tokens: 7, output_tokens: 3 }, error: { message: "Custom continuation unavailable" } });
      if (path !== "/v1/responses") throw new Error("Unexpected direct custom tool execution");
      inference++;
      return inference === 1 ? json({ id: "resp_custom", status: variant === "background" ? "queued" : "completed", ...(variant === "background" ? {} : { output: calls }) }) : inference === 2 ? queuedFailure ? json({ id: "resp_custom_failed", status: "queued" }) : json({ error: { message: "Custom continuation unavailable" } }, 503) : json({ id: "resp_custom_done", status: "completed", output_text: "Custom results received" });
    });
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.click(screen.getByLabelText("Stream response"));
    if (variant === "browser") await userEvent.click(screen.getByLabelText("Use API session management"));
    await userEvent.click(screen.getByText("Advanced parameters")); fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: advanced } });
    await userEvent.type(screen.getByLabelText("Message"), "Review custom query"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    if (variant === "background") {
      await screen.findByRole("region", { name: "Background response" });
      fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: JSON.stringify({ tools: [{ ...tools[0], format: { type: "grammar", syntax: "regex", definition: "changed" } }] }) } });
      await userEvent.click(screen.getByRole("button", { name: "Refresh background response" }));
    }
    const review = await screen.findByRole("region", { name: "Tool approvals" }); expect(review).toHaveTextContent('owner:"demo"');
    expect(screen.getByLabelText("Message")).toBeDisabled(); expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Execute query" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Custom tool result custom_1"), { target: { value: originalResult } });
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Use result for query" }));
    await userEvent.click(screen.getAllByRole("button", { name: "Decline query" })[1]);
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeEnabled();
    fireEvent.change(screen.getByLabelText("Custom tool result custom_1"), { target: { value: originalResult + " updated" } });
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Custom tool result custom_1"), { target: { value: originalResult } });
    await userEvent.click(screen.getByRole("button", { name: "Use result for query" })); expect(inference).toBe(1);
    if (variant !== "background") fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: JSON.stringify({ tools: [{ ...tools[0], description: "Changed" }] }) } });
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("custom tool definition changed"); expect(inference).toBe(1);
    fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: advanced } });
    await userEvent.click(screen.getByRole("button", { name: "Get code" }));
    const code = await screen.findByLabelText("Request code"); expect(code).toHaveTextContent("custom_tool_call_output"); expect(code).toHaveTextContent("9007199254740993"); expect(code).not.toHaveTextContent("playground-token");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    if (queuedFailure) { await screen.findByRole("region", { name: "Background response" }); expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(inference).toBe(2); await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); }
    expect(await screen.findByRole("alert")).toHaveTextContent("Custom continuation unavailable"); expect(screen.getByRole("alert")).not.toHaveTextContent("idempotency");
    expect(screen.getByLabelText("Custom tool result custom_1")).toHaveValue(originalResult);
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" })); await screen.findByText("Custom results received");
    expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(screen.getAllByText("Review custom query")).toHaveLength(1);
    const bodies = mock.mock.calls.filter(([path]) => path === "/v1/responses").map(([, options]) => JSON.parse(String(options?.body)));
    const results = [{ type: "custom_tool_call_output", call_id: "custom_1", output: originalResult }, { type: "custom_tool_call_output", call_id: "custom_2", output: '{"isError":true,"error":"User declined tool invocation"}' }];
    expect(bodies).toHaveLength(3); expect(bodies[1]).toEqual(bodies[2]); expect(bodies[2].tools).toEqual(tools);
    expect(bodies[2].input).toEqual(variant === "browser" ? [{ role: "user", content: "Review custom query" }, ...calls, ...results] : results);
    expect(bodies[2].previous_response_id).toBe(variant === "browser" ? undefined : "resp_custom");
  });
  it("fails closed for an undeclared custom call while keeping actual output, usage and Clear", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : new Response(JSON.stringify({ id: "resp_custom_foreign", status: "completed", output_text: "Actual provider text", output: [{ type: "custom_tool_call", call_id: "foreign", name: "query", input: "untrusted code" }], usage: { input_tokens: 9, output_tokens: 2 } }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "Original prompt"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("submitted request"); expect(screen.getByText("Actual provider text")).toBeInTheDocument();
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("9"); expect(screen.getByLabelText("Message")).toBeDisabled(); expect(screen.getByRole("button", { name: "Get code" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Clear" })); expect(screen.getByLabelText("Message")).toBeEnabled();
  });
  it("confirms empty custom output explicitly, rejects oversized results and clears drafts when credentials change", async () => {
    const calls = [{ type: "custom_tool_call", call_id: "custom_empty", name: "query", input: "" }];
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => String(path).endsWith("/models") ? new Response('{"data":[{"id":"model"}]}') : new Response(JSON.stringify({ id: "resp_custom", status: "completed", output: calls }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.click(screen.getByText("Advanced parameters")); fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"tools":[{"type":"custom","name":"query"}]}' } });
    await userEvent.type(screen.getByLabelText("Message"), "Custom query"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    await screen.findByRole("region", { name: "Tool approvals" });
    await userEvent.click(screen.getByRole("button", { name: "Use empty result for query" })); expect(screen.getByText("Empty tool result")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeEnabled();
    fireEvent.change(screen.getByLabelText("Custom tool result custom_empty"), { target: { value: "🙂".repeat(32769) } });
    expect(screen.getByRole("alert")).toHaveTextContent("128 KiB"); expect(screen.getByRole("alert")).not.toHaveTextContent("Execution may already");
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled(); expect(screen.getByRole("button", { name: "Use empty result for query" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Custom tool result custom_empty"), { target: { value: "Private draft" } });
    await userEvent.click(screen.getByRole("button", { name: "Use result for query" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Virtual key source" })); await userEvent.click(screen.getByRole("option", { name: "Test API key" }));
    await userEvent.type(screen.getByLabelText("Test API key"), "new-test-key"); await userEvent.click(screen.getByRole("button", { name: "Apply connection" }));
    await waitFor(() => expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument());
    expect(screen.queryByDisplayValue("Private draft")).not.toBeInTheDocument(); expect(screen.getByLabelText("Message")).toBeEnabled();
    expect(mock.mock.calls.filter(([path]) => path === "/v1/responses")).toHaveLength(1);
  });
  it("blocks new turns for queued Responses, preserves failed refreshes and commits final output once", async () => {
    let inference = 0, reads = 0;
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return json({ data: [{ id: "model" }] });
      if (path === "/v1/responses/resp_job") {
        reads++; return reads === 1 ? json({ error: { message: "Read unavailable" } }, 503) : reads === 2 ? json({ id: "resp_job", status: "in_progress" }) : json({ id: "resp_job", status: "completed", output_text: "Final job answer", usage: { input_tokens: 0, output_tokens: 3 } });
      }
      inference++; return inference === 1 ? json({ id: "resp_job", status: "queued", usage: { input_tokens: 0, output_tokens: 0 } }) : json({ id: "resp_next", status: "completed", output_text: "Next answer" });
    });
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.click(screen.getByLabelText("Stream response")); await userEvent.click(screen.getByText("Advanced parameters"));
    fireEvent.change(screen.getByLabelText("Advanced parameters JSON"), { target: { value: '{"background":true}' } });
    await userEvent.type(screen.getByLabelText("Message"), "Original job prompt"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("region", { name: "Background response" })).toHaveTextContent("queued"); expect(screen.getByLabelText("Message")).toBeDisabled();
    expect(screen.queryByText("No text output")).not.toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("—");
    await userEvent.click(screen.getByRole("button", { name: "Refresh background response" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Read unavailable"); expect(screen.getByRole("region", { name: "Background response" })).toHaveTextContent("queued");
    await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); await waitFor(() => expect(screen.getByRole("region", { name: "Background response" })).toHaveTextContent("in_progress"));
    await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); await screen.findByText("Final job answer");
    expect(screen.getAllByText("Original job prompt")).toHaveLength(1); expect(screen.queryByRole("region", { name: "Background response" })).not.toBeInTheDocument();
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0");
    await userEvent.type(screen.getByLabelText("Message"), "Follow up"); await userEvent.click(screen.getByRole("button", { name: "Send message" })); await screen.findByText("Next answer");
    const requests = mock.mock.calls.filter(([path]) => path === "/v1/responses"); expect(requests).toHaveLength(2);
    expect(JSON.parse(String(requests[1][1]?.body))).toMatchObject({ previous_response_id: "resp_job" });
  });
  it("cancels a known background job explicitly and preserves cancellation status and usage", async () => {
    const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? json({ data: [{ id: "model" }] }) : String(path).endsWith("/cancel") ? json({ id: "resp_job", status: "cancelled", usage: { input_tokens: 2, output_tokens: 0 } }) : json({ id: "resp_job", status: "queued" }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "Job"); await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByRole("region", { name: "Background response" });
    await userEvent.click(screen.getByRole("button", { name: "Cancel background response" })); expect(await screen.findByRole("alert")).toHaveTextContent("cancelled"); expect(screen.getByLabelText("Message")).toBeEnabled();
    const call = mock.mock.calls.find(([path]) => path === "/v1/responses/resp_job/cancel")!; expect(call[1]?.method).toBe("POST"); expect(call[1]?.body).toBeUndefined();
    expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("0");
  });
  it("keeps a cancellation acknowledgement pending until the server reports a terminal status", async () => {
    const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? json({ data: [{ id: "model" }] }) : String(path).endsWith("/cancel") ? json({ id: "resp_job", status: "in_progress" }) : path === "/v1/responses/resp_job" ? json({ id: "resp_job", status: "cancelled" }) : json({ id: "resp_job", status: "queued" }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "Job"); await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByRole("region", { name: "Background response" });
    await userEvent.click(screen.getByRole("button", { name: "Cancel background response" })); await waitFor(() => expect(screen.getByRole("region", { name: "Background response" })).toHaveTextContent("in_progress"));
    expect(screen.getByLabelText("Message")).toBeDisabled(); await userEvent.click(screen.getByRole("button", { name: "Refresh background response" })); expect(await screen.findByRole("alert")).toHaveTextContent("cancelled"); expect(screen.getByLabelText("Message")).toBeEnabled();
  });
  it("ignores a late lifecycle read after clearing the conversation", async () => {
    let resolve!: (value: Response) => void;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : String(path).includes("resp_job") ? new Promise((done) => resolve = done) : new Response('{"id":"resp_job","status":"queued"}', { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "Job"); await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByRole("region", { name: "Background response" });
    await userEvent.click(screen.getByRole("button", { name: "Refresh background response" }));
    await userEvent.click(screen.getByLabelText("Endpoint")); await userEvent.click(screen.getByRole("option", { name: "/v1/chat/completions" }));
    await act(async () => resolve(new Response('{"id":"resp_job","status":"completed","output_text":"Private late answer"}')));
    expect(screen.queryByText("Private late answer")).not.toBeInTheDocument(); expect(screen.queryByRole("region", { name: "Background response" })).not.toBeInTheDocument();
  });
  it.each(["chat", "responses-api", "responses-browser"])("requires approval and continues %s with typed results and no phantom user", async (variant) => {
    const endpoint = variant === "chat" ? "chat" : "responses";
    let inference = 0, toolAttempts = 0;
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return json({ data: [{ id: "model" }] });
      if (String(path).endsWith("/tools")) return json({ tools: [{ name: "lookup", inputSchema: { type: "object" } }] });
      if (String(path).endsWith("/tools/lookup")) { toolAttempts++; return toolAttempts === 1 ? json({ error: { message: "Temporarily unavailable" } }, 503) : json({ content: [{ type: "text", text: "Found" }] }); }
      inference++;
      return inference === 1 ? json(endpoint === "chat" ? { choices: [{ message: { content: null, tool_calls: [{ id: "call_1", type: "function", function: { name: "lookup", arguments: '{"query":"demo"}' } }] } }] } : { id: "resp_1", output: [{ type: "function_call", call_id: "call_1", name: "lookup", arguments: '{"query":"demo"}' }] }) : json(endpoint === "chat" ? { choices: [{ message: { content: "Final answer" } }] } : { id: "resp_2", output_text: "Final answer" });
    });
    authenticated(); await screen.findByText("1 authorized model");
    if (endpoint === "responses") await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    if (variant === "responses-browser") await userEvent.click(screen.getByLabelText("Use API session management"));
    await userEvent.click(screen.getByText("Tools, resources and policies"));
    await userEvent.type(screen.getByLabelText("Tool discovery server"), "weather");
    await userEvent.click(screen.getByRole("button", { name: "Load MCP tools" }));
    await userEvent.click(await screen.findByLabelText("MCP function lookup"));
    await userEvent.type(screen.getByLabelText("Message"), "Look up demo"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    await screen.findByRole("region", { name: "Tool approvals" });
    expect(toolAttempts).toBe(0); expect(screen.getByLabelText("Message")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Execute lookup" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Temporarily unavailable");
    await userEvent.click(screen.getByRole("button", { name: "Retry lookup" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Continue with tool results" })).toBeEnabled());
    const toolCalls = mock.mock.calls.filter(([path]) => String(path).endsWith("/tools/lookup"));
    expect(new Headers(toolCalls[0][1]?.headers).get("Idempotency-Key")).toBe(new Headers(toolCalls[1][1]?.headers).get("Idempotency-Key"));
    await userEvent.click(screen.getByRole("button", { name: "Get code" }));
    expect(await screen.findByLabelText("Request code")).toHaveTextContent(endpoint === "chat" ? "tool_call_id" : "function_call_output");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" }));
    await screen.findByText("Final answer"); expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument();
    const body = JSON.parse(String(mock.mock.calls.filter(([path]) => path === (endpoint === "chat" ? "/v1/chat/completions" : "/v1/responses"))[1][1]?.body));
    if (endpoint === "chat") { expect(body.messages.at(-1)).toMatchObject({ role: "tool", tool_call_id: "call_1" }); expect(body.messages.filter((item: { role: string }) => item.role === "user")).toHaveLength(1); }
    else { expect(body.input.at(-1)).toMatchObject({ type: "function_call_output", call_id: "call_1" }); expect(body.input.some((item: { role?: string }) => item.role === "user")).toBe(variant === "responses-browser"); expect(body.previous_response_id).toBe(variant === "responses-api" ? "resp_1" : undefined); }
  });
  it("allows declining an unbound tool without contacting any MCP server", async () => {
    let inference = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : new Response(JSON.stringify(++inference === 1 ? { choices: [{ message: { content: null, tool_calls: [{ id: "call", type: "function", function: { name: "unknown", arguments: "{}" } }] } }] } : { choices: [{ message: { content: "Declined acknowledged" } }] }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.type(screen.getByLabelText("Message"), "Question"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("button", { name: "Execute unknown" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Decline unknown" }));
    await userEvent.click(screen.getByRole("button", { name: "Continue with tool results" })); await screen.findByText("Declined acknowledged");
    expect(mock.mock.calls.some(([path]) => String(path).includes("/mcp/"))).toBe(false);
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body)).messages.at(-1).content).toContain("User declined");
  });
  it("preserves selected policies across workspace tabs, and resets them when refreshed models change scope", async () => {
    let model = "first";
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return new Response(JSON.stringify({ data: [{ id: model }] }));
      if (String(path).includes("catalog")) return new Response('{"mcp_servers":[],"mcp_toolsets":[],"policies":["strict"],"tags":[],"agents":[],"truncated":false}');
      return new Response('{"allowed":false}');
    });
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByText("Tools, resources and policies")); await userEvent.click(screen.getByRole("button", { name: "Load resource catalog" })); await userEvent.click(await screen.findByLabelText("Prompt policy strict"));
    await userEvent.click(screen.getByRole("tab", { name: "Compliance" })); await userEvent.click(screen.getByRole("tab", { name: "Chat" }));
    await userEvent.type(screen.getByLabelText("Message"), "Check preserved"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("blocked by policy strict");
    model = "second"; await userEvent.click(screen.getByRole("button", { name: "Refresh models" })); await waitFor(() => expect(screen.getByLabelText("Model")).toHaveTextContent("second"));
    await userEvent.click(screen.getByText("Tools, resources and policies")); expect(screen.queryByText(/Active selections/)).not.toBeInTheDocument();
    expect(mock.mock.calls.filter(([path]) => path === "/guardrails/apply_guardrail")).toHaveLength(1);
  });
  it("discards a late approved tool result after an endpoint change clears the conversation", async () => {
    let resolve!: (response: Response) => void;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return new Response('{"data":[{"id":"model"}]}');
      if (String(path).endsWith("/tools")) return new Response('{"tools":[{"name":"lookup","inputSchema":{}}]}');
      if (String(path).endsWith("/tools/lookup")) return new Promise<Response>((done) => resolve = done);
      return new Response('{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}', { headers: { "Content-Type": "application/json" } });
    });
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByText("Tools, resources and policies")); await userEvent.type(screen.getByLabelText("Tool discovery server"), "weather"); await userEvent.click(screen.getByRole("button", { name: "Load MCP tools" })); await userEvent.click(await screen.findByLabelText("MCP function lookup"));
    await userEvent.type(screen.getByLabelText("Message"), "Question"); await userEvent.click(screen.getByRole("button", { name: "Run request" })); await userEvent.click(await screen.findByRole("button", { name: "Execute lookup" }));
    await userEvent.click(screen.getByLabelText("Endpoint")); await userEvent.click(screen.getByRole("option", { name: "/v1/responses" }));
    await act(async () => resolve(new Response('{"content":[{"type":"text","text":"Late"}]}')));
    expect(screen.queryByRole("region", { name: "Tool approvals" })).not.toBeInTheDocument(); expect(screen.queryByText("Late")).not.toBeInTheDocument();
    expect(mock.mock.calls.filter(([path]) => String(path).endsWith("/tools/lookup"))).toHaveLength(1);
  });

  it("stops generation when an additional prompt policy blocks or fails, and exports preflight checks", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return new Response('{"data":[{"id":"model"}]}');
      if (String(path).includes("playground/catalog")) return new Response('{"mcp_servers":[],"mcp_toolsets":[],"agents":[],"tags":[],"policies":["strict"],"truncated":false}');
      return new Response('{"allowed":false}');
    });
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.click(screen.getByText("Tools, resources and policies"));
    await userEvent.click(screen.getByRole("button", { name: "Load resource catalog" }));
    await userEvent.click(await screen.findByLabelText("Prompt policy strict"));
    await userEvent.type(screen.getByLabelText("Message"), "Check prompt");
    await userEvent.click(screen.getByRole("button", { name: "Get code" }));
    expect(await screen.findByLabelText("Request code")).toHaveTextContent("apply_guardrail");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("blocked by policy strict");
    expect(mock.mock.calls.some(([path]) => path === "/v1/chat/completions")).toBe(false);
    expect(screen.getByLabelText("Message")).toHaveValue("Check prompt");
    mock.mockImplementation(async () => new Response('{"error":{"message":"Scanner unavailable"}}', { status: 503 }));
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Scanner unavailable");
    expect(mock.mock.calls.some(([path]) => path === "/v1/chat/completions")).toBe(false);
  });
  it("clears the browser file input after success so the same file can be selected again", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : new Response('{"choices":[{"message":{"content":"Done"}}]}', { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model");
    const input = screen.getByLabelText("Conversation attachments") as HTMLInputElement;
    const file = new File(["%PDF-test"], "report.pdf", { type: "application/pdf" });
    await userEvent.upload(input, file); await screen.findByRole("button", { name: "Remove conversation attachments" });
    expect(input.files).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Run request" })); await screen.findByText("Done");
    expect(input).toHaveValue(""); expect(input.files).toHaveLength(0);
    await userEvent.upload(input, file); expect(await screen.findByRole("button", { name: "Remove conversation attachments" })).toBeInTheDocument();
  });
  it.each(["chat", "responses"])("sends image/PDF attachments in the %s dialect and clears successful attachments", async (endpoint) => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : new Response(JSON.stringify(endpoint === "chat" ? { choices: [{ message: { content: "Attachment answer" } }] } : { output_text: "Attachment answer" }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model");
    if (endpoint === "responses") await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    fireEvent.change(screen.getByLabelText("Conversation attachments"), { target: { files: [new File(["image"], "input.png", { type: "image/png" }), new File(["%PDF-test"], "report.pdf", { type: "application/pdf" })] } });
    await screen.findByRole("button", { name: "Remove conversation attachments" });
    await userEvent.type(screen.getByLabelText("Message"), "Describe"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("Attachment answer")).toBeInTheDocument();
    const request = JSON.parse(String(mock.mock.calls.find(([path]) => path === (endpoint === "chat" ? "/v1/chat/completions" : "/v1/responses"))![1]?.body));
    const parts = endpoint === "chat" ? request.messages[0].content : request.input[0].content;
    expect(parts[1].type).toBe(endpoint === "chat" ? "image_url" : "input_image"); expect(parts[2].type).toBe("input_file");
    expect(screen.queryByRole("button", { name: "Remove conversation attachments" })).not.toBeInTheDocument();
  });
  it("shows a cost estimate using the entered rates and provider usage", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/models" ? new Response('{"data":[{"id":"model"}]}') : new Response('{"choices":[{"message":{"content":"Answer"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}', { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model"); await userEvent.click(screen.getByText("Cost estimate"));
    await userEvent.type(screen.getByLabelText("Input price per million tokens"), "2"); await userEvent.type(screen.getByLabelText("Output price per million tokens"), "8");
    await userEvent.type(screen.getByLabelText("Message"), "Estimate"); await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("0.006000 USD")).toBeInTheDocument();
  });
  it("does not turn a failed Responses stream into a successful conversation or continuation", async () => {
    let attempt = 0;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "model-a" }] }));
      attempt++;
      return attempt === 1 ? streamResponse(['event: response.failed\ndata: {"type":"response.failed","response":{"id":"failed-id","status":"failed","error":{"message":"Upstream unavailable"}}}\n\n'])
        : new Response(JSON.stringify({ id: "valid-id", output: [{ type: "message", content: [{ type: "output_text", text: "Recovered" }] }] }), { headers: { "Content-Type": "application/json" } });
    });
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "retry me");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Upstream unavailable");
    expect(screen.getByText("failed-id")).toBeInTheDocument(); expect(screen.getByText("Status").nextElementSibling).toHaveTextContent("failed");
    expect(screen.getByLabelText("Message")).toHaveValue("retry me");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("Recovered")).toBeInTheDocument();
    const body = JSON.parse(String(fetchMock.mock.calls.filter(([path]) => path === "/v1/responses")[1][1]?.body));
    expect(body).not.toHaveProperty("previous_response_id");
  });

  it("validates generation controls before sending and keeps the draft on failure", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [{ id: "model-a" }] })));
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.clear(screen.getByLabelText("Maximum output tokens"));
    await userEvent.type(screen.getByLabelText("Maximum output tokens"), "0");
    await userEvent.type(screen.getByLabelText("Message"), "Keep this prompt");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Maximum output tokens must be an integer");
    expect(fetchMock.mock.calls.filter(([path]) => String(path) !== "/v1/models")).toHaveLength(0);
    expect(screen.getByLabelText("Message")).toHaveValue("Keep this prompt");
  });

  it("keeps the active session when an untrusted custom URL is rejected before transport", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [{ id: "model-a" }] })));
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.click(screen.getByLabelText("Virtual key source"));
    await userEvent.click(screen.getByRole("option", { name: "Test API key" }));
    await userEvent.type(screen.getByLabelText("Test API key"), "independent-test-key");
    await userEvent.type(screen.getByLabelText("Custom gateway base URL"), "https://untrusted.example.test/v1");
    await userEvent.click(screen.getByRole("button", { name: "Apply connection" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("not trusted");
    expect(fetchMock.mock.calls.every(([path]) => String(path) === "/v1/models")).toBe(true);
    expect(screen.getByText(/Active: current UI session/)).toBeInTheDocument();
    expect(screen.getByLabelText("Test API key")).toHaveValue("independent-test-key");
  });

  it("tests another key independently and excludes it from storage and exported code", async () => {
    const meta = document.createElement("meta"); meta.name = "ai-gateway-playground-origins"; meta.content = '["https://other.example.test"]'; document.head.append(meta);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ data: [{ id: "model-a" }] })));
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.click(screen.getByLabelText("Virtual key source"));
    await userEvent.click(screen.getByRole("option", { name: "Test API key" }));
    await userEvent.type(screen.getByLabelText("Test API key"), "independent-test-key");
    await userEvent.type(screen.getByLabelText("Custom gateway base URL"), "https://other.example.test/v1");
    await userEvent.click(screen.getByRole("button", { name: "Apply connection" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "https://other.example.test/v1/models")).toBe(true));
    await screen.findByText("1 authorized model");
    const call = fetchMock.mock.calls.find(([path]) => path === "https://other.example.test/v1/models")!;
    expect(new Headers(call[1]?.headers).get("Authorization")).toBe("Bearer independent-test-key");
    expect(call[1]?.credentials).toBe("omit");
    await userEvent.click(screen.getByRole("button", { name: "Get code" }));
    const code = await screen.findByLabelText("Request code");
    expect(code).toHaveTextContent("GATEWAY_API_KEY"); expect(code).not.toHaveTextContent("independent-test-key");
    expect(JSON.stringify({ ...sessionStorage, ...window.localStorage })).not.toContain("independent-test-key");
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Get code" })).toHaveFocus();
  });

  it("discards a late model list from the previous credential even if fetch ignores abort", async () => {
    let resolveFirst!: (value: Response) => void;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const header = new Headers(options?.headers).get("Authorization");
      if (header === "Bearer playground-token") return new Promise<Response>((resolve) => { resolveFirst = resolve; });
      return new Response(JSON.stringify({ data: [{ id: "new-model" }] }));
    });
    authenticated();
    await userEvent.click(screen.getByLabelText("Virtual key source"));
    await userEvent.click(screen.getByRole("option", { name: "Test API key" }));
    await userEvent.type(screen.getByLabelText("Test API key"), "new-key");
    await userEvent.click(screen.getByRole("button", { name: "Apply connection" }));
    await screen.findByText("1 authorized model");
    resolveFirst(new Response(JSON.stringify({ data: [{ id: "old-model" }] })));
    await waitFor(() => expect(screen.getByLabelText("Model")).toHaveTextContent("new-model"));
    expect(screen.getByLabelText("Model")).not.toHaveTextContent("old-model");
  });

  it("keeps tool arguments and reasoning deltas out of Responses text", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/v1/models"
      ? new Response(JSON.stringify({ data: [{ id: "model-a" }] }))
      : streamResponse([
        'event: response.function_call_arguments.delta\ndata: {"type":"response.function_call_arguments.delta","delta":"tool-arguments"}\n\n',
        'event: response.reasoning_summary_text.delta\ndata: {"type":"response.reasoning_summary_text.delta","delta":"private-reasoning"}\n\n',
        'event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"Public answer"}\n\n',
        'event: response.completed\ndata: {"type":"response.completed","response":{"id":"resp-ok","usage":{"input_tokens":0,"output_tokens":2,"total_tokens":2}}}\n\n'
      ]));
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "question");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("Public answer")).toBeInTheDocument();
    const conversation = screen.getByRole("region", { name: "Playground conversation" });
    expect(conversation.querySelector(".assistant > pre")).toHaveTextContent("Public answer");
    expect(conversation.querySelector(".assistant > pre")).not.toHaveTextContent("tool-arguments");
    expect(conversation.querySelector(".assistant > pre")).not.toHaveTextContent("private-reasoning");
    expect(screen.getByText("Reasoning").closest("details")).toHaveTextContent("private-reasoning");
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0");
  });

  it("uses browser history when Responses API session management is disabled", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/v1/models"
      ? new Response(JSON.stringify({ data: [{ id: "model-a" }] }))
      : new Response(JSON.stringify({ id: "resp-one", output_text: "Answer" }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Message"), "first");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    await screen.findByText("Answer");
    await userEvent.click(screen.getByLabelText("Use API session management"));
    await userEvent.type(screen.getByLabelText("Message"), "second");
    await userEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path]) => path === "/v1/responses")).toHaveLength(2));
    const body = JSON.parse(String(fetchMock.mock.calls.filter(([path]) => path === "/v1/responses")[1][1]?.body));
    expect(body).not.toHaveProperty("previous_response_id");
    expect(body.input).toEqual([{ role: "user", content: "first" }, { role: "assistant", content: "Answer" }, { role: "user", content: "second" }]);
  });

  it("submits with Enter while Shift+Enter keeps a newline in the draft", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => String(input) === "/v1/models"
      ? new Response(JSON.stringify({ data: [{ id: "model-a" }] }))
      : new Response(JSON.stringify({ choices: [{ message: { content: "Keyboard answer" } }] }), { headers: { "Content-Type": "application/json" } }));
    authenticated(); await screen.findByText("1 authorized model");
    await userEvent.type(screen.getByLabelText("Message"), "first");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}second");
    expect(screen.getByLabelText("Message")).toHaveValue("first\nsecond");
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByText("Keyboard answer")).toBeInTheDocument();
    const call = fetchMock.mock.calls.find(([path]) => path === "/v1/chat/completions")!;
    expect(JSON.parse(String(call[1]?.body)).messages).toEqual([{ role: "user", content: "first\nsecond" }]);
  });

  it("discovers authorized models and renders incremental Chat Completions output", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "gpt-z" }, { id: "gpt-a" }] }), { status: 200 });
      return streamResponse([
        'data: {"id":"chat-1","model":"gpt-a","choices":[{"delta":{"content":"Hel"}}]}\n\n',
        'data: {"id":"chat-1","model":"gpt-a","choices":[{"delta":{"content":"lo"}}],"usage":{"total_tokens":3}}\n\ndata: [DONE]\n\n'
      ]);
    });
    authenticated();
    expect(await screen.findByText("2 authorized models")).toBeInTheDocument();
    expect(screen.getByLabelText("Model")).toHaveTextContent("gpt-a");
    await userEvent.click(screen.getByLabelText("Model"));
    expect(screen.getByRole("option", { name: "gpt-z" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: "gpt-a" }));
    expect(screen.getByLabelText("Instructions")).toHaveClass("g-text-area__control");
    expect(screen.getByLabelText("Message")).toHaveClass("g-text-area__control");
    await userEvent.type(screen.getByLabelText("Instructions"), "Be concise");
    await userEvent.type(screen.getByLabelText("Message"), "hello");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("Hello")).toBeInTheDocument();
    expect(screen.getByText("chat-1")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();

    const request = fetchMock.mock.calls.find(([path]) => String(path) === "/v1/chat/completions")!;
    const body = JSON.parse(String(request[1]?.body));
    expect(body).toMatchObject({ model: "gpt-a", stream: true, max_completion_tokens: 256, messages: [{ role: "system", content: "Be concise" }, { role: "user", content: "hello" }] });
    expect(new Headers(request[1]?.headers).get("X-Session-ID")).toMatch(/^playground-/);

    await userEvent.type(screen.getByLabelText("Message"), "again");
    await userEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path]) => String(path) === "/v1/chat/completions")).toHaveLength(2));
    const continuation = fetchMock.mock.calls.filter(([path]) => String(path) === "/v1/chat/completions")[1];
    expect(JSON.parse(String(continuation[1]?.body)).messages).toEqual([
      { role: "system", content: "Be concise" },
      { role: "user", content: "hello" },
      { role: "assistant", content: "Hello" },
      { role: "user", content: "again" }
    ]);
  });

  it("uses a JSON Responses fallback without replaying the request and preserves previous_response_id", async () => {
    let responseNumber = 0;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      if (String(input) === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "response-model" }] }), { status: 200 });
      responseNumber++;
      return new Response(JSON.stringify({ id: `resp-${responseNumber}`, model: "response-model", output_text: responseNumber === 1 ? "First" : "Second", usage: { total_tokens: responseNumber + 1 } }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    authenticated();
    await screen.findByText("1 authorized model");
    expect(screen.getByLabelText("Model")).toHaveTextContent("response-model");
    await userEvent.click(screen.getByRole("tab", { name: "Responses API" }));
    await userEvent.type(screen.getByLabelText("Instructions"), "Use plain text");
    await userEvent.type(screen.getByLabelText("Message"), "first question");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    expect(await screen.findByText("First")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Message"), "second question");
    await userEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(await screen.findByText("Second")).toBeInTheDocument();

    const requests = fetchMock.mock.calls.filter(([path]) => String(path) === "/v1/responses");
    expect(requests).toHaveLength(2);
    expect(JSON.parse(String(requests[0][1]?.body))).toMatchObject({ input: "first question", instructions: "Use plain text", stream: true, max_output_tokens: 256 });
    expect(JSON.parse(String(requests[1][1]?.body))).toMatchObject({ input: "second question", previous_response_id: "resp-1" });
    expect(new Headers(requests[0][1]?.headers).get("X-Session-ID")).toBe(new Headers(requests[1][1]?.headers).get("X-Session-ID"));
  });

  it("cancels an in-flight stream through AbortSignal", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      if (String(input) === "/v1/models") return new Response(JSON.stringify({ data: [{ id: "slow-model" }] }), { status: 200 });
      return new Promise<Response>((_resolve, reject) => {
        options?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      });
    });
    authenticated();
    await screen.findByText("1 authorized model");
    expect(screen.getByLabelText("Model")).toHaveTextContent("slow-model");
    await userEvent.type(screen.getByLabelText("Message"), "wait");
    await userEvent.click(screen.getByRole("button", { name: "Run request" }));
    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Request cancelled");
    await waitFor(() => expect(screen.getByRole("button", { name: "Run request" })).toBeEnabled());
  });
});
