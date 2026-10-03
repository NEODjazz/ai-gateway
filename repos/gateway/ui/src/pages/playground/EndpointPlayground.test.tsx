import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { EndpointPlayground } from "./EndpointPlayground";
import { playgroundConnection } from "./requests";
import type { SpecializedEndpoint } from "./endpointRequests";

function setup(endpoint: SpecializedEndpoint) { return render(<EndpointPlayground endpoint={endpoint} connection={playgroundConnection(new APIClient(() => "test-key"), "session", "", "")} models={["model"]} connectionControls={null} connectionChanged={false} />); }
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
  it("transcribes the supplied audio and displays the transcript", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"text":"Transcript text","segments":[]}'));
    setup("transcription"); fireEvent.change(screen.getByLabelText("Endpoint attachment"), { target: { files: [new File(["audio"], "input.wav", { type: "audio/wav" })] } });
    await screen.findByText("input.wav · audio/wav"); await run(); expect(await screen.findByText("Transcript text")).toBeInTheDocument();
    expect(JSON.parse(String(mock.mock.calls[0][1]?.body))).toMatchObject({ file: { filename: "input.wav", media_type: "audio/wav" }, response_format: "verbose_json" });
  });
  it("does not interpret a JSON-RPC error as an empty successful agent response", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"jsonrpc":"2.0","error":{"code":-32005,"message":"Agent unavailable"}}'));
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
