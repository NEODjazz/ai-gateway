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
