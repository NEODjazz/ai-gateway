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

describe("PlaygroundPage", () => {
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
    expect(screen.queryByText("failed-id")).not.toBeInTheDocument();
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

  it("tests another key independently and excludes it from storage and exported code", async () => {
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
