import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { playgroundConnection } from "./requests";
import { RealtimePlayground } from "./RealtimePlayground";
import { startRealtimeRecorder } from "./realtimeRecorder";
vi.mock("./realtimeRecorder", () => ({ startRealtimeRecorder: vi.fn() }));
class Socket {
  static OPEN = 1;
  static instances: Socket[] = [];
  readyState = 0; bufferedAmount = 0;
  onopen?: () => void; onmessage?: (message: { data: string }) => void; onclose?: (event: { wasClean: boolean }) => void; onerror?: () => void;
  sent: Record<string, unknown>[] = [];
  constructor(readonly url: string, readonly protocols: string[]) { Socket.instances.push(this); }
  send(text: string) { this.sent.push(JSON.parse(text)); }
  close = vi.fn(() => { this.readyState = 3; });
  open() { this.readyState = 1; this.onopen?.(); }
  emit(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
}
function connection() { return playgroundConnection(new APIClient(() => "private-test-key"), "session", "", ""); }
const ready = async () => {
  await userEvent.click(screen.getByRole("button", { name: "Connect Realtime" }));
  await waitFor(() => expect(Socket.instances).toHaveLength(1)); const socket = Socket.instances[0];
  act(() => socket.open()); expect(screen.getByLabelText("Realtime connection status")).toHaveTextContent("configuring");
  act(() => socket.emit({ type: "session.updated" })); expect(screen.getByLabelText("Realtime connection status")).toHaveTextContent("connected"); return socket;
};
const send = async () => { await userEvent.type(screen.getByLabelText("Realtime message"), "Question"); await userEvent.keyboard("{Enter}"); };
const setup = () => render(<RealtimePlayground connection={connection()} models={["model"]} connectionControls={null} connectionChanged={false} />);
beforeEach(() => {
  Socket.instances = []; vi.stubGlobal("WebSocket", Socket);
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ticket: "a".repeat(43), protocol: "ai-gateway.realtime.v1", socket_path: "/v1/realtime/browser", expires_at: new Date(Date.now() + 20000).toISOString() })));
});
afterEach(() => { vi.unstubAllGlobals(); vi.mocked(startRealtimeRecorder).mockReset(); });
describe("Realtime Playground", () => {
  it("waits for configuration, sends typed text once and displays finalized usage including zero", async () => {
    setup(); expect(screen.getByLabelText("Realtime message")).toBeDisabled(); const socket = await ready();
    expect(socket.sent[0]).toMatchObject({ type: "session.update", session: { output_modalities: ["text"] } });
    await send(); expect(socket.sent.slice(1)).toMatchObject([{ type: "conversation.item.create", item: { role: "user", content: [{ type: "input_text", text: "Question" }] } }, { type: "response.create", response: { max_output_tokens: 256 } }]);
    expect(screen.getByLabelText("Realtime message")).toBeDisabled();
    act(() => { socket.emit({ type: "response.created", response: { id: "r1" } }); socket.emit({ type: "response.output_text.delta", response_id: "r1", delta: "Partial" }); });
    expect(screen.getByText("Partial")).toBeInTheDocument();
    act(() => socket.emit({ type: "response.done", response: { id: "r1", status: "completed", output: [{ content: [{ type: "output_text", text: "Final answer" }] }], usage: { input_tokens: 0, output_tokens: 4 } } }));
    expect(screen.getByText("Final answer")).toBeInTheDocument(); expect(screen.queryByText("Partial")).not.toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("0"); expect(screen.getByText("Audio input tokens").nextElementSibling).toHaveTextContent("—");
    expect(screen.getByLabelText("Realtime message")).not.toBeDisabled();
  });
  it("queues cancellation until response identity arrives and waits for final usage", async () => {
    setup(); const socket = await ready(); await send();
    await userEvent.click(screen.getByRole("button", { name: "Cancel Realtime response" })); expect(socket.sent.some((event) => event.type === "response.cancel")).toBe(false);
    act(() => socket.emit({ type: "response.created", response: { id: "r1" } })); expect(socket.sent.at(-1)).toMatchObject({ type: "response.cancel", response_id: "r1" });
    expect(screen.getByLabelText("Realtime message")).toBeDisabled();
    act(() => socket.emit({ type: "response.done", response: { id: "r1", status: "cancelled", usage: { input_tokens: 1, output_tokens: 0 } } }));
    expect(screen.getByText("Response status").nextElementSibling).toHaveTextContent("cancelled"); expect(screen.getByText("Output tokens").nextElementSibling).toHaveTextContent("0");
  });
  it("fails closed on malformed or mismatching response frames", async () => {
    setup(); const socket = await ready(); await send();
    act(() => { socket.emit({ type: "response.created", response: { id: "r1" } }); socket.emit({ type: "response.done", response: { id: "another", status: "completed" } }); });
    expect(screen.getByRole("alert")).toHaveTextContent("mismatches"); expect(socket.close).toHaveBeenCalled(); expect(screen.getByLabelText("Realtime connection status")).toHaveTextContent("disconnected");
  });
  it("disconnects and clears private results/code on credential change; late frames cannot replace new state", async () => {
    const view = setup(); const socket = await ready(); await send(); const late = socket.onmessage;
    await userEvent.click(screen.getByRole("button", { name: "Get Realtime code" })); expect(screen.getByLabelText("Realtime code")).not.toHaveTextContent("private-test-key");
    view.rerender(<RealtimePlayground connection={connection()} models={["model"]} connectionControls={null} connectionChanged={false} />);
    act(() => late?.({ data: JSON.stringify({ type: "response.output_text.delta", delta: "Private stale answer" }) }));
    expect(socket.close).toHaveBeenCalled(); expect(screen.queryByText("Private stale answer")).not.toBeInTheDocument(); expect(screen.getByLabelText("Realtime conversation history")).not.toHaveTextContent("Question"); expect(screen.queryByLabelText("Realtime code")).not.toBeInTheDocument();
  });
  it("does not open a socket after a late ticket arrives for a discarded connection", async () => {
    let resolve!: (response: Response) => void; vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise((done) => resolve = done));
    const view = setup(); await userEvent.click(screen.getByRole("button", { name: "Connect Realtime" }));
    view.rerender(<RealtimePlayground connection={connection()} models={["model"]} connectionControls={null} connectionChanged={false} />);
    await act(async () => resolve(new Response('{}'))); expect(Socket.instances).toHaveLength(0); expect(screen.getByLabelText("Realtime connection status")).toHaveTextContent("disconnected");
  });
  it("records only after a user action, sends PCM buffer explicitly and plays bounded audio manually", async () => {
    let samples!: (bytes: Uint8Array) => void;
    const stop = vi.fn(async (_flush: boolean) => {}); vi.mocked(startRealtimeRecorder).mockImplementation(async (_signal, callback) => { samples = callback; return { stop }; });
    const create = vi.fn((_blob: Blob) => "blob:synthetic-audio"), revoke = vi.fn(); Object.defineProperty(URL, "createObjectURL", { configurable: true, value: create }); Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revoke });
    const view = setup();
    await userEvent.click(screen.getByLabelText("Realtime mode")); await userEvent.click(await screen.findByText("Voice and text"));
    const socket = await ready(); expect(startRealtimeRecorder).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Record voice" })); await screen.findByText("Microphone: recording");
    act(() => samples(new Uint8Array(4800))); expect(socket.sent.at(-1)).toMatchObject({ type: "input_audio_buffer.append" }); expect(socket.sent.some((event) => event.type === "response.create")).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Stop and send voice" })); expect(stop).toHaveBeenCalledWith(true); expect(socket.sent.slice(-2)).toMatchObject([{ type: "input_audio_buffer.commit" }, { type: "response.create", response: { max_output_tokens: 256 } }]);
    act(() => { socket.emit({ type: "response.created", response: { id: "r1" } }); socket.emit({ type: "response.output_audio.delta", response_id: "r1", delta: "AQACAA==" }); socket.emit({ type: "response.done", response: { id: "r1", status: "completed", output: [{ content: [{ type: "output_audio", transcript: "Spoken answer" }] }], usage: { input_tokens: 5, output_tokens: 7, input_token_details: { audio_tokens: 3 }, output_token_details: { audio_tokens: 6 } } } }); });
    const audio = screen.getByLabelText("Realtime audio 2"); expect(audio).toHaveAttribute("controls"); expect(audio).not.toHaveAttribute("autoplay"); expect(create.mock.calls[0][0].type).toBe("audio/wav");
    expect(screen.getByText("Audio input tokens").nextElementSibling).toHaveTextContent("3"); expect(screen.getByText("Audio output tokens").nextElementSibling).toHaveTextContent("6");
    view.unmount(); expect(revoke).toHaveBeenCalledWith("blob:synthetic-audio"); Reflect.deleteProperty(URL, "createObjectURL"); Reflect.deleteProperty(URL, "revokeObjectURL");
  });
  it("discards a pending microphone permission request on disconnect and never sends audio afterwards", async () => {
    let resolve!: (value: { stop: (flush: boolean) => Promise<void> }) => void; let samples!: (bytes: Uint8Array) => void;
    vi.mocked(startRealtimeRecorder).mockImplementation((_signal, callback) => { samples = callback; return new Promise((done) => resolve = done); });
    setup(); await userEvent.click(screen.getByLabelText("Realtime mode")); await userEvent.click(await screen.findByText("Voice and text")); const socket = await ready();
    await userEvent.click(screen.getByRole("button", { name: "Record voice" })); await userEvent.click(screen.getByRole("button", { name: "Disconnect Realtime" }));
    const stop = vi.fn(async () => {}); await act(async () => resolve({ stop })); act(() => samples(new Uint8Array(4800)));
    expect(stop).toHaveBeenCalledWith(false); expect(socket.sent.some((event) => event.type === "input_audio_buffer.append")).toBe(false);
  });
  it("bounds pending microphone permission requests and cannot let a discarded attempt stop a later capture", async () => {
    let resolve!: (value: { stop: (flush: boolean) => Promise<void> }) => void;
    const nextStop = vi.fn(async () => {});
    vi.mocked(startRealtimeRecorder).mockImplementationOnce(() => new Promise((done) => resolve = done)).mockResolvedValue({ stop: nextStop });
    setup(); await userEvent.click(screen.getByLabelText("Realtime mode")); await userEvent.click(await screen.findByText("Voice and text")); await ready();
    await userEvent.click(screen.getByRole("button", { name: "Record voice" })); await userEvent.click(screen.getByRole("button", { name: "Discard recording" }));
    await userEvent.click(screen.getByRole("button", { name: "Record voice" })); expect(screen.getByRole("alert")).toHaveTextContent("previous browser microphone request"); expect(startRealtimeRecorder).toHaveBeenCalledOnce();
    const previousStop = vi.fn(async () => {}); await act(async () => resolve({ stop: previousStop })); expect(previousStop).toHaveBeenCalledWith(false);
    await userEvent.click(screen.getByRole("button", { name: "Record voice" })); await screen.findByText("Microphone: recording"); expect(nextStop).not.toHaveBeenCalled();
  });
  it("disconnects before sending when the socket buffer exceeds its limit", async () => {
    setup(); const socket = await ready(); socket.bufferedAmount = 1024 * 1024 + 1; await send();
    expect(screen.getByRole("alert")).toHaveTextContent("send buffer exceeded"); expect(socket.sent.some((event) => event.type === "response.create")).toBe(false);
  });
  it("retains partial text with an interrupted label when transport fails", async () => {
    setup(); const socket = await ready(); await send();
    act(() => { socket.emit({ type: "response.created", response: { id: "r1" } }); socket.emit({ type: "response.output_text.delta", response_id: "r1", delta: "Partial retained" }); socket.onerror?.(); });
    expect(screen.getByLabelText("Realtime conversation history")).toHaveTextContent("Partial retained");
    expect(screen.getByText("assistant · interrupted")).toBeInTheDocument(); expect(screen.getByText("Input tokens").nextElementSibling).toHaveTextContent("—");
  });
  it("shows ticket errors and never presents an unconfigured connection as ready", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"error":{"message":"Encryption unavailable"}}', { status: 503 })); setup(); await userEvent.click(screen.getByRole("button", { name: "Connect Realtime" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Encryption unavailable"); expect(Socket.instances).toHaveLength(0);
  });
});
