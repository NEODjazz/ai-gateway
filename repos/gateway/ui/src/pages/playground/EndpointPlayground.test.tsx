import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { EndpointPlayground } from "./EndpointPlayground";
import { playgroundConnection } from "./requests";
import type { SpecializedEndpoint } from "./endpointRequests";

function setup(endpoint: SpecializedEndpoint) { return render(<EndpointPlayground endpoint={endpoint} connection={playgroundConnection(new APIClient(() => "test-key"), "session", "", "")} models={["model"]} connectionControls={null} connectionChanged={false} />); }
const nativeJSON = (value: unknown) => new Response(typeof value === "string" ? value : JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
const run = () => userEvent.click(screen.getByRole("button", { name: "Run endpoint request" }));
describe("Endpoint Playground", () => {
  it.each(["json", "fallback", "stream"])("shows failed Interactions output and actual usage over %s without retrying", async (transport) => {
    const response = { id: "interaction_failed", status: "failed", steps: [{ type: "model_output", content: [{ type: "text", text: "Retained failed native answer" }] }], usage: { total_input_tokens: 0, total_output_tokens: 3 }, error: { message: "Provider execution failed" } };
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(transport === "stream"
      ? new Response(`data: ${JSON.stringify({ event_type: "interaction.failed", interaction: response })}\n\n`, { headers: { "Content-Type": "text/event-stream" } })
      : nativeJSON(response));
    setup("interactions"); if (transport === "json") await userEvent.click(screen.getByLabelText("Stream native response"));
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Billed failed native prompt"); await run();
    expect(await screen.findByRole("alert")).toHaveTextContent("Interaction failed. Provider execution failed"); expect(screen.getByText("Retained failed native answer")).toBeInTheDocument();
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0"); expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("3");
    expect(screen.getByText("Status").nextElementSibling).toHaveTextContent("failed"); expect(screen.queryByRole("region", { name: "Background interaction" })).not.toBeInTheDocument(); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Clear endpoint output" })); expect(screen.queryByText("Retained failed native answer")).not.toBeInTheDocument(); expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
  it("restores the prompt and attachments after a background Interaction fails without advancing continuity", async () => {
    let creates = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/interactions/interaction_background"
      ? nativeJSON({ id: "interaction_background", status: "failed", usage: { total_input_tokens: 7, total_output_tokens: 0 }, error: { message: "Background failed" } })
      : nativeJSON(++creates === 1 ? { id: "interaction_background", status: "queued" } : { id: "interaction_retry", status: "completed", steps: [{ type: "model_output", content: [{ type: "text", text: "Retry succeeded" }] }] }));
    setup("interactions"); await userEvent.click(screen.getByLabelText("Stream native response")); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"background":true}' } });
    await userEvent.upload(screen.getByLabelText("Endpoint attachment"), new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], "kept.png", { type: "image/png" }));
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Retained background prompt"); await run();
    await screen.findByRole("region", { name: "Background interaction" }); expect(screen.queryByText("kept.png · image/png")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh background interaction" })); expect(await screen.findByRole("alert")).toHaveTextContent("Background failed");
    expect(screen.getByLabelText("Endpoint input")).toHaveValue("Retained background prompt"); expect(screen.getByText("kept.png · image/png")).toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("7"); expect(creates).toBe(1);
    await run(); await screen.findByText("Retry succeeded");
    const requests = mock.mock.calls.filter(([path]) => path === "/v1/interactions"); expect(requests).toHaveLength(2); expect(requests[1][1]?.body).toBe(requests[0][1]?.body); expect(screen.queryByRole("region", { name: "Failed interaction output" })).not.toBeInTheDocument();
  });
  it.each(["messages", "interactions_api", "interactions_browser", "interactions_background", "interactions_api_failed", "interactions_browser_failed"])("preserves exact %s arguments, binds definitions and keeps manual results on retry", async (variant) => {
    const reportedFailure = variant.endsWith("_failed"); variant = variant.replace("_failed", "");
    const endpoint = variant === "messages" ? "messages" : "interactions";
    const tool = endpoint === "messages" ? { name: "query", input_schema: { type: "object", properties: { id: { type: "integer" } } } } : { type: "function", name: "query", parameters: { type: "object", properties: { id: { type: "integer" } } } };
    const advanced = JSON.stringify({ tools: [tool], ...(variant === "interactions_browser" ? { store: false } : {}), ...(variant === "interactions_background" ? { background: true } : {}) });
    const args = '{ "id":9007199254740993,"amount":0.1234567890123456789012345 }';
    const response = endpoint === "messages" ? `{"content":[{"type":"text","text":"Actual native answer"},{"type":"tool_use","id":"call","name":"query","input":${args}}],"usage":{"input_tokens":8,"output_tokens":3}}`
      : `{"id":"native_job","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"Actual native answer"}]},{"type":"function_call","id":"call","name":"query","arguments":${args}}],"usage":{"total_input_tokens":8,"total_output_tokens":3}}`;
    const final = endpoint === "messages" ? { content: [{ type: "text", text: "Native result received" }] } : { id: "next", status: "completed", steps: [{ type: "model_output", content: [{ type: "text", text: "Native result received" }] }] };
    let creates = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/interactions/native_job") return nativeJSON(response);
      if (path !== `/v1/${endpoint}`) throw new Error("Unexpected direct tool execution");
      return ++creates === 1 ? nativeJSON(variant === "interactions_background" ? { id: "native_job", status: "queued" } : response)
        : creates === 2 ? reportedFailure ? nativeJSON({ id: "interaction_failed_continuation", status: "failed", steps: [{ type: "model_output", content: [{ type: "text", text: "Partial native continuation output" }] }], usage: { total_input_tokens: 7, total_output_tokens: 3 }, error: { message: "Native continuation unavailable" } }) : new Response('{"error":{"message":"Native continuation unavailable"}}', { status: 503 }) : nativeJSON(final);
    });
    setup(endpoint);
    if (variant === "interactions_background") await userEvent.click(screen.getByLabelText("Stream native response"));
    fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: advanced } });
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Review native query"); await run();
    if (variant === "interactions_background") {
      await screen.findByRole("region", { name: "Background interaction" });
      fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"tools":[]}' } });
      await userEvent.click(screen.getByRole("button", { name: "Refresh background interaction" }));
    }
    expect(await screen.findByText(args)).toBeInTheDocument();
    const history = screen.getByRole("region", { name: "Native conversation history" });
    expect(history).toHaveTextContent("9007199254740993"); expect(history).toHaveTextContent("0.1234567890123456789012345"); expect(history).not.toHaveTextContent("9007199254740992");
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("8");
    expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Tool result call"), { target: { value: 'rows: 9007199254740993\n<not-json>' } });
    if (variant !== "interactions_background") fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"tools":[]}' } });
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("definition changed"); expect(creates).toBe(1);
    fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: advanced } });
    await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" }));
    const code = await screen.findByLabelText("Request code");
    expect(code).toHaveTextContent("9007199254740993"); if (endpoint === "messages" || variant === "interactions_browser") expect(code).toHaveTextContent("0.1234567890123456789012345"); expect(code).not.toHaveTextContent("test-key");
    await userEvent.click(screen.getByRole("tab", { name: "JavaScript" })); expect(code).not.toHaveTextContent("JSON.stringify(");
    await userEvent.click(screen.getByRole("tab", { name: "Python" })); expect(code).toHaveTextContent("body.encode()"); await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" })); expect(await screen.findByRole("alert")).toHaveTextContent("Native continuation unavailable");
    expect(screen.getByLabelText("Tool result call")).toHaveValue('rows: 9007199254740993\n<not-json>');
    if (reportedFailure) { expect(screen.getByRole("region", { name: "Failed interaction output" })).toHaveTextContent("Partial native continuation output"); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("7"); }
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" })); await screen.findByText("Native result received");
    const requests = mock.mock.calls.filter(([path]) => path === `/v1/${endpoint}`);
    expect(requests).toHaveLength(3); if (endpoint === "messages") expect(new Headers(requests[0][1]?.headers).get("anthropic-version")).toBe("2023-06-01"); expect(requests[1][1]?.body).toBe(requests[2][1]?.body);
    const body = JSON.parse(String(requests[2][1]?.body)); expect(body.tools).toEqual([tool]);
    if (endpoint === "messages") expect(String(requests[2][1]?.body)).toContain(`"input":${args}`);
    else if (variant === "interactions_browser") expect(body.input.find((item: { type: string }) => item.type === "function_call").arguments).toBe(args);
    else expect(body).toMatchObject({ previous_interaction_id: "native_job", input: [{ type: "function_call_output", call_id: "call", output: 'rows: 9007199254740993\n<not-json>' }] });
    expect(screen.getByLabelText("Endpoint input")).toBeEnabled();
  });
  it.each(["messages", "interactions"] as const)("keeps actual %s output and Usage after invalid tool review and requires Clear", async (endpoint) => {
    const call = endpoint === "messages" ? { type: "tool_use", id: "duplicate", name: "query", input: {} } : { type: "function_call", id: "duplicate", name: "query", arguments: {} };
    const response = endpoint === "messages" ? { content: [{ type: "text", text: "Retained native output" }, call, call], usage: { input_tokens: 9, output_tokens: 4 } }
      : { id: "native_job", status: "completed", steps: [{ type: "model_output", content: [{ type: "text", text: "Retained native output" }] }, call, call], usage: { total_input_tokens: 9, total_output_tokens: 4 } };
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(nativeJSON(response));
    setup(endpoint); await userEvent.type(screen.getByLabelText("Endpoint input"), "Original prompt"); await run();
    expect(await screen.findByRole("alert")).toHaveTextContent("Tool review failed"); expect(screen.getByText("Retained native output")).toBeInTheDocument();
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("9"); expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("4");
    expect(screen.getByLabelText("Endpoint input")).toBeDisabled(); expect(screen.getByRole("button", { name: "Get endpoint code" })).toBeDisabled(); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Clear endpoint output" })); expect(screen.getByLabelText("Endpoint input")).toBeEnabled(); expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
  it("blocks queued interaction turns, preserves read errors and applies completed steps exactly once", async () => {
    let reads = 0, creates = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (path === "/v1/interactions/job") { reads++; return reads === 1 ? new Response('{"error":{"message":"Read unavailable"}}', { status: 503 }) : nativeJSON({ id: "job", status: "completed", steps: [{ type: "model_output", content: [{ type: "text", text: "Final background answer" }] }], usage: { total_input_tokens: 0, total_output_tokens: 4 } }); }
      creates++; return creates === 1 ? nativeJSON({ id: "job", status: "queued", usage: { total_input_tokens: 0, total_output_tokens: 0 } }) : nativeJSON({ id: "next", status: "completed", steps: [{ type: "model_output", content: [{ type: "text", text: "Follow up answer" }] }] });
    });
    setup("interactions"); await userEvent.click(screen.getByLabelText("Stream native response")); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"background":true}' } });
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Original background prompt"); await run();
    expect(await screen.findByRole("region", { name: "Background interaction" })).toHaveTextContent("queued"); expect(screen.getByLabelText("Endpoint input")).toBeDisabled(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("—");
    await userEvent.click(screen.getByRole("button", { name: "Refresh background interaction" })); expect(await screen.findByRole("alert")).toHaveTextContent("Read unavailable"); expect(screen.getByRole("region", { name: "Background interaction" })).toHaveTextContent("queued");
    await userEvent.click(screen.getByRole("button", { name: "Refresh background interaction" })); await screen.findByText("Final background answer"); expect(screen.getAllByText("Original background prompt")).toHaveLength(1); expect(screen.queryByRole("region", { name: "Background interaction" })).not.toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0");
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Follow up"); await run(); await screen.findByText("Follow up answer");
    const requests = mock.mock.calls.filter(([path]) => path === "/v1/interactions"); expect(requests).toHaveLength(2); expect(JSON.parse(String(requests[1][1]?.body))).toMatchObject({ previous_interaction_id: "job" });
  });
  it("keeps acknowledged interaction cancellation pending until a terminal resource read", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => String(path).endsWith("/cancel") ? nativeJSON({ id: "job", status: "in_progress" }) : path === "/v1/interactions/job" ? nativeJSON({ id: "job", status: "cancelled", usage: { total_input_tokens: 2, total_output_tokens: 0 } }) : nativeJSON({ id: "job", status: "queued" }));
    setup("interactions"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Job"); await run(); await screen.findByRole("region", { name: "Background interaction" });
    await userEvent.click(screen.getByRole("button", { name: "Cancel background interaction" })); await waitFor(() => expect(screen.getByRole("region", { name: "Background interaction" })).toHaveTextContent("in_progress")); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Refresh background interaction" })); expect(await screen.findByRole("alert")).toHaveTextContent("cancelled"); expect(screen.getByLabelText("Endpoint input")).toBeEnabled(); expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("0");
    const call = mock.mock.calls.find(([path]) => path === "/v1/interactions/job/cancel")!; expect(call[1]?.method).toBe("POST"); expect(call[1]?.body).toBeUndefined();
  });
  it("drops a late interaction read after the credential scope changes", async () => {
    let resolve!: (value: Response) => void;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => path === "/v1/interactions/job" ? new Promise((done) => resolve = done) : nativeJSON({ id: "job", status: "queued" }));
    const view = setup("interactions"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Job"); await run(); await screen.findByRole("region", { name: "Background interaction" }); await userEvent.click(screen.getByRole("button", { name: "Refresh background interaction" }));
    view.rerender(<EndpointPlayground endpoint="interactions" connection={playgroundConnection(new APIClient(() => "new-test-key"), "session", "", "")} models={["model"]} connectionControls={null} connectionChanged={false} />);
    await act(async () => resolve(nativeJSON({ id: "job", status: "completed", steps: [{ type: "model_output", content: [{ type: "text", text: "Private late result" }] }] })));
    expect(screen.queryByText("Private late result")).not.toBeInTheDocument(); expect(screen.queryByRole("region", { name: "Background interaction" })).not.toBeInTheDocument();
  });
  it("renders generated image output safely and preserves the image dialect", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"data":[{"b64_json":"AA==","revised_prompt":"Revised"}]}'));
    setup("images"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Generate a skyline"); await run();
    expect(await screen.findByRole("img", { name: "Generated image 1" })).toHaveAttribute("src", "data:image/png;base64,AA==");
    expect(mock.mock.calls[0][0]).toBe("/v1/images/generations");
    expect(JSON.parse(String(mock.mock.calls[0][1]?.body))).toMatchObject({ prompt: "Generate a skyline", response_format: "b64_json" });
  });
  it("uploads image editing input without unsupported filenames", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"data":[]}'));
    setup("image-edits"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Edit");
    fireEvent.change(screen.getByLabelText("Endpoint attachment"), { target: { files: [new File(["image"], "input.png", { type: "image/png" })] } });
    await screen.findByText("input.png · image/png"); await run(); await waitFor(() => expect(mock).toHaveBeenCalledOnce());
    expect(JSON.parse(String(mock.mock.calls[0][1]?.body)).images).toEqual([{ media_type: "image/png", data_base64: "aW1hZ2U=" }]);
  });
  it("renders embedding dimensions and retains zero token usage", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"data":[{"index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":0}}'));
    setup("embeddings"); await userEvent.type(screen.getByLabelText("Embedding input"), "Embed"); await run();
    expect(await screen.findByText("Vector 0 · 2 dimensions")).toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0");
  });
  it("previews binary speech, exports binary code and revokes media on unmount", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(new Uint8Array([1, 2, 3]), { headers: { "Content-Type": "audio/mpeg" } }));
    const create = vi.fn((_blob: Blob) => "blob:audio"), revoke = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create }); Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke });
    const view = setup("speech"); await userEvent.type(screen.getByLabelText("Speech text"), "Speak"); await run();
    expect(await screen.findByLabelText("Generated speech")).toHaveAttribute("src", "blob:audio");
    await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" }));
    expect(await screen.findByLabelText("Request code")).toHaveTextContent("--output ai-gateway-output.bin");
    view.unmount(); expect(revoke).toHaveBeenCalledWith("blob:audio");
    Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL");
  });
  it("names speech downloads using the returned MIME type instead of mutable form settings", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(new Uint8Array([1]), { headers: { "Content-Type": "audio/wav" } }));
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: () => "blob:audio" });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    const view = setup("speech"); await userEvent.type(screen.getByLabelText("Speech text"), "Speak"); await run();
    expect(await screen.findByRole("link", { name: "Download speech" })).toHaveAttribute("download", "ai-gateway-speech.wav");
    view.unmount(); Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL");
  });
  it("requires explicit native tool results before continuing and preserves the assistant blocks", async () => {
    const first = { type: "message", content: [{ type: "thinking", thinking: "Plan", signature: "signed" }, { type: "text", text: "Native answer" }, { type: "tool_use", id: "call-1", name: "lookup", input: { query: "Example" } }], usage: { input_tokens: 0, output_tokens: 4 } };
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(nativeJSON(first)).mockImplementation(async () => nativeJSON('{"content":[{"type":"text","text":"Final answer"}]}'));
    setup("messages"); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}' } }); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
    expect(await screen.findByText("Native answer")).toBeInTheDocument(); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("every tool result"); expect(mock).toHaveBeenCalledOnce();
    await userEvent.type(screen.getByLabelText("Tool result call-1"), "Verified result");
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" }));
    await screen.findByText("Final answer");
    const body = JSON.parse(String(mock.mock.calls[1][1]?.body));
    expect(body.messages).toEqual([{ role: "user", content: "First" }, { role: "assistant", content: first.content }, { role: "user", content: [{ type: "tool_result", tool_use_id: "call-1", content: "Verified result" }] }]);
    expect(screen.getByLabelText("Endpoint input")).not.toBeDisabled();
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Second"); await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(3));
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body)).messages.at(-1)).toEqual({ role: "user", content: "Second" });
  });
  it("keeps pending calls after a failed native continuation and sends an explicit error on decline", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(nativeJSON('{"content":[{"type":"tool_use","id":"call","name":"lookup","input":{}}]}')).mockResolvedValueOnce(new Response('{"error":{"message":"Unavailable"}}', { status: 503 })).mockResolvedValueOnce(nativeJSON('{"content":[{"type":"text","text":"Recovered"}]}'));
    setup("messages"); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}' } }); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
    await userEvent.click(await screen.findByRole("button", { name: "Decline lookup" })); await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Unavailable"); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" })); await screen.findByText("Recovered");
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).toEqual(JSON.parse(String(mock.mock.calls[2][1]?.body)));
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body)).messages.at(-1).content[0]).toMatchObject({ tool_use_id: "call", is_error: true });
  });
  it("replays canonical interaction steps when store is false and keeps typed function results", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(nativeJSON('{"id":"not-stored","steps":[{"type":"model_output","content":[{"type":"text","text":"Step answer"}]},{"type":"function_call","id":"call","name":"lookup","arguments":{"count":1}}]}')).mockResolvedValueOnce(nativeJSON('{"steps":[{"type":"model_output","content":[{"type":"text","text":"Continued"}]}]}'));
    setup("interactions"); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"store":false,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}' } });
    await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
    expect(await screen.findByText("Step answer")).toBeInTheDocument(); expect(screen.getByText("Tool calls and results")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Tool result call"), "Result"); await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" }));
    expect(await screen.findByLabelText("Request code")).toHaveTextContent("function_call_output"); await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" })); await screen.findByText("Continued");
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).toMatchObject({ store: false, input: [{ role: "user", content: "First" }, { role: "assistant", content: [{ type: "output_text", text: "Step answer" }] }, { type: "function_call", call_id: "call", name: "lookup", arguments: '{"count":1}' }, { type: "function_call_output", call_id: "call", output: "Result" }] });
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).not.toHaveProperty("previous_interaction_id");
  });
  it("continues stored interactions by ID with only the new input", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => nativeJSON('{"id":"stored","steps":[{"type":"model_output","content":[{"type":"text","text":"Stored answer"}]}]}'));
    setup("interactions"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run(); await screen.findByText("Stored answer");
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Second"); await run(); await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).toMatchObject({ previous_interaction_id: "stored", input: "Second" });
  });
  it.each(["messages", "interactions"] as const)("sends an attachment-only %s turn in the native dialect and removes the file after success", async (endpoint) => {
    const response = endpoint === "messages" ? { content: [{ type: "text", text: "Read PDF" }] } : { steps: [{ type: "model_output", content: [{ type: "text", text: "Read PDF" }] }] };
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(nativeJSON(response));
    setup(endpoint); fireEvent.change(screen.getByLabelText("Endpoint attachment"), { target: { files: [new File(["pdf"], "input.pdf", { type: "application/pdf" })] } });
    await screen.findByText("input.pdf · application/pdf"); await run(); await screen.findByText("Read PDF");
    const body = JSON.parse(String(mock.mock.calls[0][1]?.body));
    expect(endpoint === "messages" ? body.messages[0].content : body.input[0].content).toEqual([endpoint === "messages" ? { type: "document", source: { type: "base64", media_type: "application/pdf", data: "cGRm" } } : { type: "input_file", filename: "input.pdf", file_data: "data:application/pdf;base64,cGRm" }]);
    expect(screen.queryByText("input.pdf · application/pdf")).not.toBeInTheDocument();
  });
  it("clears native tool drafts and exported conversation content when the connection changes", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(nativeJSON({ content: [{ type: "tool_use", id: "call", name: "lookup", input: {} }] }));
    const view = setup("messages"); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}' } }); await userEvent.type(screen.getByLabelText("Endpoint input"), "Private draft"); await run();
    await userEvent.type(await screen.findByLabelText("Tool result call"), "Private result"); await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" }));
    expect(await screen.findByLabelText("Request code")).toHaveTextContent("Private result");
    view.rerender(<EndpointPlayground endpoint="messages" connection={playgroundConnection(new APIClient(() => "another-test-key"), "session", "", "")} models={["model"]} connectionControls={null} connectionChanged={false} />);
    expect(screen.queryByLabelText("Request code")).not.toBeInTheDocument(); expect(screen.queryByLabelText("Tool result call")).not.toBeInTheDocument(); expect(screen.getByLabelText("Endpoint input")).toHaveValue("");
    expect(screen.getByLabelText("Native conversation history")).not.toHaveTextContent("Private draft");
  });
  it("keeps thought-only interactions out of answer text and copy controls", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(nativeJSON({ steps: [{ type: "thought", content: [{ type: "text", text: "Reasoning only" }] }] }));
    setup("interactions"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
    const history = await screen.findByLabelText("Native conversation history"); await within(history).findByText("No text output");
    expect(within(history).getByRole("button", { name: "Copy native response 2" })).toBeDisabled(); expect(within(history).getByText("Reasoning only").closest("details")).toBeInTheDocument();
  });
  it("transcribes the supplied audio and displays the transcript", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"text":"Transcript text","segments":[]}'));
    setup("transcription"); fireEvent.change(screen.getByLabelText("Endpoint attachment"), { target: { files: [new File(["audio"], "input.wav", { type: "audio/wav" })] } });
    await screen.findByText("input.wav · audio/wav"); await run(); expect(await screen.findByText("Transcript text")).toBeInTheDocument();
    expect(JSON.parse(String(mock.mock.calls[0][1]?.body))).toMatchObject({ file: { filename: "input.wav", media_type: "audio/wav" }, response_format: "verbose_json" });
  });
  it("sends the mandatory A2A version and includes it in exported code", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => new Headers(options?.headers).get("A2A-Version") === "1.0" ? nativeJSON({ jsonrpc: "2.0", id: JSON.parse(String(options?.body)).id, result: { message: { parts: [{ text: "Versioned response" }] } } }) : new Response('{"error":{"message":"Version not supported"}}', { status: 400 }));
    setup("a2a"); await userEvent.type(screen.getByLabelText("Agent ID"), "research"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Question");
    await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" })); expect(await screen.findByLabelText("Request code")).toHaveTextContent("A2A-Version: 1.0"); await userEvent.keyboard("{Escape}");
    await run(); await screen.findByText("Versioned response"); expect(mock).toHaveBeenCalledOnce();
  });
  it("refreshes a pending A2A task and continues the same task/context with attachments", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const request = JSON.parse(String(options?.body));
      const refreshing = request.method === "GetTask", continuing = request.params.message?.taskId === "task";
      const task = { id: "task", contextId: "context", status: { state: refreshing || continuing ? "TASK_STATE_COMPLETED" : "TASK_STATE_WORKING" }, artifacts: [{ parts: [{ text: refreshing ? "Refreshed answer" : continuing ? "Continued answer" : "Working answer" }] }] };
      return nativeJSON({ jsonrpc: "2.0", id: request.id, result: refreshing ? task : { task } });
    });
    setup("a2a"); await userEvent.type(screen.getByLabelText("Agent ID"), "research"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await userEvent.keyboard("{Enter}");
    await screen.findByText("Working answer"); expect(screen.getByLabelText("Endpoint input")).toBeDisabled(); expect(screen.getByRole("button", { name: "Run endpoint request" })).toBeDisabled(); expect(mock).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" })); expect(await screen.findByLabelText("Request code")).toHaveTextContent("GetTask"); await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Refresh endpoint task" })); await screen.findByText("Refreshed answer"); expect(screen.queryByText("Working answer")).not.toBeInTheDocument(); expect(screen.getByLabelText("Endpoint input")).not.toBeDisabled();
    fireEvent.change(screen.getByLabelText("Endpoint attachment"), { target: { files: [new File(["pdf"], "brief.pdf", { type: "application/pdf" })] } }); await screen.findByText("brief.pdf · application/pdf");
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Second"); await run(); await screen.findByText("Continued answer");
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).toMatchObject({ method: "GetTask", params: { tenant: "research", id: "task" } });
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body))).toMatchObject({ method: "SendMessage", params: { message: { taskId: "task", contextId: "context", parts: [{ text: "Second" }, { raw: "cGRm", mediaType: "application/pdf", filename: "brief.pdf" }] } } });
    expect(screen.queryByText("brief.pdf · application/pdf")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Agent conversation history")).toHaveTextContent("First"); expect(screen.getByLabelText("Agent conversation history")).toHaveTextContent("Second");
  });
  it("keeps a known A2A task after a refresh failure and cancels it only on explicit action", async () => {
    let count = 0;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => {
      const request = JSON.parse(String(options?.body)); count++;
      if (count === 2) return nativeJSON({ jsonrpc: "2.0", id: request.id, error: { message: "Refresh unavailable" } });
      return nativeJSON({ jsonrpc: "2.0", id: request.id, result: { task: { id: "task", contextId: "context", status: { state: request.method === "CancelTask" ? "TASK_STATE_CANCELED" : "TASK_STATE_WORKING" }, artifacts: [] } } });
    });
    setup("a2a"); await userEvent.type(screen.getByLabelText("Agent ID"), "research"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
    await userEvent.click(await screen.findByRole("button", { name: "Refresh endpoint task" })); expect(await screen.findByRole("alert")).toHaveTextContent("Refresh unavailable"); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel endpoint task" })); await waitFor(() => expect(screen.getByLabelText("Agent task state")).toHaveTextContent("TASK_STATE_CANCELED"));
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body))).toMatchObject({ method: "CancelTask", params: { id: "task" } }); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Clear endpoint output" })); expect(screen.getByLabelText("Endpoint input")).not.toBeDisabled(); expect(screen.queryByLabelText("Agent task state")).not.toBeInTheDocument();
  });
  it("clears A2A history on agent change and rejects a mismatched RPC response", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => nativeJSON({ jsonrpc: "2.0", id: JSON.parse(String(options?.body)).id, result: { message: { parts: [{ text: "Independent answer" }] } } }));
    setup("a2a"); await userEvent.type(screen.getByLabelText("Agent ID"), "research"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run(); await screen.findByText("Independent answer"); expect(screen.getByRole("status")).toHaveTextContent("each request is independent");
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Draft"); fireEvent.change(screen.getByLabelText("Agent ID"), { target: { value: "other" } });
    expect(screen.queryByText("Independent answer")).not.toBeInTheDocument(); expect(screen.getByLabelText("Endpoint input")).toHaveValue("");
    mock.mockResolvedValueOnce(nativeJSON({ jsonrpc: "2.0", id: "wrong", result: { message: { parts: [{ text: "Wrong response" }] } } }));
    await userEvent.type(screen.getByLabelText("Endpoint input"), "Second"); await run(); expect(await screen.findByRole("alert")).toHaveTextContent("invalid agent response"); expect(screen.queryByText("Wrong response")).not.toBeInTheDocument();
  });
  it("does not interpret a JSON-RPC error as an empty successful agent response", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_path, options) => nativeJSON({ jsonrpc: "2.0", id: JSON.parse(String(options?.body)).id, error: { code: -32005, message: "Agent unavailable" } }));
    setup("a2a"); await userEvent.type(screen.getByLabelText("Agent ID"), "research"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Question"); await run();
    expect(await screen.findByRole("alert")).toHaveTextContent("Agent unavailable"); expect(screen.queryByText("Agent response")).not.toBeInTheDocument();
  });
  it("executes MCP only after an explicit button click and includes idempotency in code", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"content":[{"type":"text","text":"Tool result"}],"isError":false}'));
    setup("mcp"); await userEvent.type(screen.getByLabelText("MCP server ID"), "server"); await userEvent.type(screen.getByLabelText("MCP tool name"), "lookup"); expect(mock).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Get endpoint code" })); expect(await screen.findByLabelText("Request code")).toHaveTextContent("Idempotency-Key"); await userEvent.keyboard("{Escape}"); expect(mock).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Execute MCP tool" })); expect(await screen.findByText("Tool result")).toBeInTheDocument();
    expect(new Headers(mock.mock.calls[0][1]?.headers).get("Idempotency-Key")).toMatch(/^playground-/);
  });
  it("ignores late output after clearing an in-flight request", async () => {
    let resolve!: (value: Response) => void;
    vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((done) => resolve = done));
    const view = setup("embeddings"); await userEvent.type(screen.getByLabelText("Embedding input"), "Embed"); await run(); view.unmount();
    await act(async () => resolve(new Response('{"data":[{"embedding":[1,2,3]}]}')));
    expect(screen.queryByText("Embedding vectors")).not.toBeInTheDocument();
  });
});

describe("Native tool review failures", () => {
  it.each(["messages", "interactions"] as const)("keeps %s finalized output and usage when streamed tool arguments contain invalid JSON", async (endpoint) => {
    const events = endpoint === "messages" ? [
      { type: "message_start", message: { usage: { input_tokens: 5 } } },
      { type: "content_block_start", index: 0, content_block: { type: "text", text: "Completed native text" } },
      { type: "content_block_start", index: 1, content_block: { type: "tool_use", id: "call", name: "query", input: {} } },
      { type: "content_block_delta", index: 1, delta: { type: "input_json_delta", partial_json: "{" } },
      { type: "message_delta", usage: { output_tokens: 2 } }, { type: "message_stop" }
    ] : [
      { event_type: "step.start", index: 0, step: { type: "model_output", content: [{ type: "text", text: "Completed native text" }] } },
      { event_type: "step.start", index: 1, step: { type: "function_call", id: "call", name: "query" } },
      { event_type: "step.delta", index: 1, delta: { type: "arguments_delta", arguments: "{" } },
      { event_type: "interaction.completed", interaction: { id: "job", status: "completed", usage: { total_input_tokens: 5, total_output_tokens: 2 } } }
    ];
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(""), { headers: { "Content-Type": "text/event-stream" } }));
    setup(endpoint); await userEvent.type(screen.getByLabelText("Endpoint input"), "Review malformed tool"); await run();
    expect(await screen.findByRole("alert")).toHaveTextContent("Tool review failed"); expect(screen.getByText("Completed native text")).toBeInTheDocument();
    expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("5"); expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("2"); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
  });
  it("only allows declining an undeclared native function", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(nativeJSON({ content: [{ type: "tool_use", id: "call", name: "foreign", input: {} }] })).mockResolvedValueOnce(nativeJSON({ content: [{ type: "text", text: "Decline received" }] }));
    setup("messages"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Review unknown"); await run();
    expect(await screen.findByLabelText("Tool result call")).toBeDisabled(); expect(screen.getByRole("status")).toHaveTextContent("only be declined");
    await userEvent.click(screen.getByRole("button", { name: "Decline foreign" })); await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" })); await screen.findByText("Decline received");
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body)).messages.at(-1).content[0]).toMatchObject({ tool_use_id: "call", is_error: true });
  });
});
