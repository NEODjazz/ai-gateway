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
    setup("messages"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
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
    setup("messages"); await userEvent.type(screen.getByLabelText("Endpoint input"), "First"); await run();
    await userEvent.click(await screen.findByRole("button", { name: "Decline lookup" })); await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Unavailable"); expect(screen.getByLabelText("Endpoint input")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Continue native tool results" })); await screen.findByText("Recovered");
    expect(JSON.parse(String(mock.mock.calls[1][1]?.body))).toEqual(JSON.parse(String(mock.mock.calls[2][1]?.body)));
    expect(JSON.parse(String(mock.mock.calls[2][1]?.body)).messages.at(-1).content[0]).toMatchObject({ tool_use_id: "call", is_error: true });
  });
  it("replays canonical interaction steps when store is false and keeps typed function results", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(nativeJSON('{"id":"not-stored","steps":[{"type":"model_output","content":[{"type":"text","text":"Step answer"}]},{"type":"function_call","id":"call","name":"lookup","arguments":{"count":1}}]}')).mockResolvedValueOnce(nativeJSON('{"steps":[{"type":"model_output","content":[{"type":"text","text":"Continued"}]}]}'));
    setup("interactions"); fireEvent.change(screen.getByLabelText("Endpoint parameters JSON"), { target: { value: '{"store":false}' } });
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
    const view = setup("messages"); await userEvent.type(screen.getByLabelText("Endpoint input"), "Private draft"); await run();
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
