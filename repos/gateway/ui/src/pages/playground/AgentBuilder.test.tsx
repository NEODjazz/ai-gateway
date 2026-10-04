import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { AgentBuilder } from "./AgentBuilder";
import { playgroundConnection } from "./requests";
import type { AgentProfile } from "./agents";

const connection = () => playgroundConnection(new APIClient(() => "test-agent-key"), "session", "", "");
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const profile: AgentProfile = { id: "writer", name: "Writer", model: "model", instructions_configured: true, generation: { temperature: 0, max_output_tokens: 400 }, tool_policy_id: "policy", allowed_tools: ["lookup"], max_tool_calls: 3, max_iterations: 2, tags: ["team"], enabled: true, execution_supported: true };
function setup(value = connection()) { return render(<AgentBuilder connection={value} models={["model"]} connectionChanged={false} connectionControls={null} />); }
function fixtures() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (url, options) => {
    if (url === "/admin/v1/agent-profiles") return json({ data: [profile] });
    if (url === "/admin/v1/tool-policies") return json({ data: [{ id: "policy", name: "Read policy", enabled: true, allowed_tools: ["lookup"], max_tool_calls: 3 }] });
    if (String(url).startsWith("/a2a/")) { const body = JSON.parse(String(options?.body)); return json({ jsonrpc: "2.0", id: body.id, result: { message: { role: "ROLE_AGENT", parts: [{ text: "Saved agent answer" }] } } }); }
    if (options?.method === "DELETE") return new Response(null, { status: 204 });
    if (options?.method === "PUT") { const body = JSON.parse(String(options.body)); return json({ ...profile, ...body, id: String(url).split("/").at(-1), instructions_configured: !!body.instructions, instructions: undefined }); }
    return json({ profile, instructions: "Private saved instructions" });
  });
}
async function selectAgent() {
  await waitFor(() => expect(screen.getByRole("button", { name: "New agent" })).toBeEnabled());
  await userEvent.click(screen.getByLabelText("Saved agent"));
  await userEvent.click(screen.getByRole("option", { name: "Writer · model" }));
  await waitFor(() => expect(screen.getByLabelText("Agent instructions")).toHaveValue("Private saved instructions"));
}

describe("Agent Builder", () => {
  it("reads full configuration, preserves settings on edit and executes only saved A2A configuration", async () => {
    const mock = fixtures(); setup(); await selectAgent();
    expect(screen.getByLabelText("Agent temperature")).toHaveValue(0); expect(screen.getByLabelText("Agent maximum output tokens")).toHaveValue(400);
    await userEvent.type(screen.getByLabelText("Agent instructions"), " updated");
    await userEvent.click(within(screen.getByRole("tablist", { name: "Agent Builder view" })).getByRole("tab", { name: "Chat" }));
    expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled(); expect(mock.mock.calls.some(([url]) => String(url).startsWith("/a2a/"))).toBe(false);
    await userEvent.click(screen.getByRole("tab", { name: "Configure" })); await userEvent.click(screen.getByRole("button", { name: "Save agent" }));
    await screen.findByText("Agent saved. Chat and tests use this saved configuration.");
    const put = mock.mock.calls.find(([, options]) => options?.method === "PUT");
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ instructions: "Private saved instructions updated", generation: { temperature: 0, max_output_tokens: 400 } });
    await userEvent.click(screen.getByRole("tab", { name: "Chat" })); await userEvent.type(screen.getByLabelText("Agent prompt"), "Hello{enter}");
    expect(await screen.findByText("Saved agent answer")).toBeInTheDocument();
    const request = mock.mock.calls.find(([url]) => url === "/a2a/writer");
    expect(new Headers(request?.[1]?.headers).get("A2A-Version")).toBe("1.0"); expect(String(request?.[1]?.body)).not.toContain("Private saved instructions");
    expect(screen.getByText(/Stateless agent response/)).toBeInTheDocument();
  });
  it("creates a profile, refuses ID collisions and confirms exact profile deletion", async () => {
    const mock = fixtures(); setup(); await waitFor(() => expect(screen.getByRole("button", { name: "New agent" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "New agent" }));
    await userEvent.type(screen.getByLabelText("Agent ID"), "writer"); await userEvent.type(screen.getByLabelText("Agent name"), "New writer");
    await userEvent.click(screen.getByRole("button", { name: "Save agent" })); expect(await screen.findByRole("alert")).toHaveTextContent("already exists");
    expect(mock.mock.calls.some(([, options]) => options?.method === "PUT")).toBe(false);
    await userEvent.clear(screen.getByLabelText("Agent ID")); await userEvent.type(screen.getByLabelText("Agent ID"), "new-writer");
    await userEvent.click(screen.getByRole("button", { name: "Save agent" })); await screen.findByText("Agent saved. Chat and tests use this saved configuration.");
    expect(screen.getByLabelText("Agent ID")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Delete agent" }));
    expect(mock.mock.calls.some(([, options]) => options?.method === "DELETE")).toBe(false);
    expect(screen.getByRole("dialog", { name: "Delete agent" })).toHaveTextContent("new-writer");
    await userEvent.click(screen.getByRole("button", { name: "Confirm delete agent" })); await screen.findByText("Agent removed.");
    expect(mock.mock.calls.find(([, options]) => options?.method === "DELETE")?.[0]).toBe("/admin/v1/agent-profiles/new-writer");
  });
  it("keeps encryption errors visible and does not present failed saves as executable", async () => {
    const mock = fixtures(); setup(); await selectAgent();
    mock.mockImplementationOnce(async () => json({ error: { message: "Configuration encryption unavailable" } }, 503));
    await userEvent.type(screen.getByLabelText("Agent instructions"), " edit"); await userEvent.click(screen.getByRole("button", { name: "Save agent" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Configuration encryption unavailable");
    await userEvent.click(screen.getByRole("tab", { name: "Chat" })); expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled();
  });
  it("reports authorization failures without fabricated profiles", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => json({ error: { message: "Administrator role required" } }, 403)); setup();
    expect(await screen.findByRole("alert")).toHaveTextContent("Administrator role required");
    await userEvent.click(screen.getByRole("tab", { name: "Chat" })); expect(screen.getByText("Select or save an agent before using Chat.")).toBeInTheDocument();
  });
  it("discards old credential detail and late execution responses", async () => {
    const mock = fixtures(), old = connection(), view = setup(old); await selectAgent();
    await userEvent.click(screen.getByRole("tab", { name: "Chat" }));
    let resolve: (response: Response) => void = () => undefined, bodyID = "";
    mock.mockImplementationOnce((_url, options) => { bodyID = JSON.parse(String(options?.body)).id; return new Promise<Response>((done) => { resolve = done; }); });
    await userEvent.type(screen.getByLabelText("Agent prompt"), "Old request"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" }));
    view.rerender(<AgentBuilder connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
    await act(async () => resolve(json({ jsonrpc: "2.0", id: bodyID, result: { message: { parts: [{ text: "Late old response" }] } } })));
    expect(screen.queryByText("Late old response")).not.toBeInTheDocument(); expect(screen.getByLabelText("Agent instructions")).toHaveValue("");
  });
  it("exports connection code without instructions or the active key", async () => {
    fixtures(); setup(); await selectAgent(); await userEvent.click(screen.getByRole("tab", { name: "Connect" }));
    await userEvent.click(screen.getByRole("button", { name: "Get connection code" }));
    const code = screen.getByLabelText("Request code"); expect(code).toHaveTextContent("A2A-Version"); expect(code).toHaveTextContent("GATEWAY_API_KEY");
    expect(code).not.toHaveTextContent("test-agent-key"); expect(code).not.toHaveTextContent("Private saved instructions");
  });
  it("ignores a delayed configuration read after changing credentials", async () => {
    const mock = fixtures(), view = setup();
    await waitFor(() => expect(screen.getByRole("button", { name: "New agent" })).toBeEnabled());
    let resolve: (response: Response) => void = () => undefined;
    mock.mockImplementationOnce(() => new Promise<Response>((done) => { resolve = done; }));
    await userEvent.click(screen.getByLabelText("Saved agent")); await userEvent.click(screen.getByRole("option", { name: "Writer · model" }));
    view.rerender(<AgentBuilder connection={connection()} models={["model"]} connectionChanged={false} connectionControls={null} />);
    await act(async () => resolve(json({ profile, instructions: "Old credential configuration" })));
    await waitFor(() => expect(screen.getByRole("button", { name: "New agent" })).toBeEnabled());
    expect(screen.getByLabelText("Agent instructions")).toHaveValue(""); expect(screen.getByLabelText("Saved agent")).not.toHaveTextContent("Writer");
  });
  it("refreshes the selected configuration rather than presenting an old draft as current", async () => {
    const mock = fixtures(); setup(); await selectAgent();
    mock.mockImplementation(async (url) => {
      if (url === "/admin/v1/agent-profiles") return json({ data: [{ ...profile, name: "Updated writer" }] });
      if (url === "/admin/v1/tool-policies") return json({ data: [{ id: "policy", name: "Read policy", enabled: true, allowed_tools: ["lookup"], max_tool_calls: 3 }] });
      return json({ profile: { ...profile, name: "Updated writer" }, instructions: "Updated saved instructions" });
    });
    await userEvent.click(screen.getByRole("button", { name: "Refresh agents" }));
    await waitFor(() => expect(screen.getByLabelText("Agent instructions")).toHaveValue("Updated saved instructions"));
    expect(screen.getByLabelText("Agent name")).toHaveValue("Updated writer"); expect(screen.getByRole("button", { name: "Save agent" })).toBeDisabled();
  });
  it("runs explicit bounded batch tests and reports partial failures", async () => {
    const mock = fixtures(); setup(); await selectAgent(); await userEvent.click(screen.getByRole("tab", { name: "Batch Test" }));
    mock.mockImplementation(async (_url, options) => { const body = JSON.parse(String(options?.body)); return body.params.message.parts[0].text === "Bad" ? json({ jsonrpc: "2.0", id: body.id, error: { message: "Provider failed" } }) : json({ jsonrpc: "2.0", id: body.id, result: { message: { parts: [{ text: "Successful test" }] } } }); });
    await userEvent.type(screen.getByLabelText("Agent batch prompts"), "Good\nBad");
    expect(mock.mock.calls.filter(([url]) => String(url).startsWith("/a2a/"))).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: "Run agent tests" }));
    expect(await screen.findByText("Successful test")).toBeInTheDocument(); expect(await screen.findByRole("alert")).toHaveTextContent("Provider failed");
    expect(screen.getByRole("button", { name: "Export agent results" })).toBeEnabled(); expect(screen.getByRole("status")).toHaveTextContent("1 completed · 0 pending · 1 failed");
  });
  it("continues with task identity and handles direct GetTask results", async () => {
    const mock = fixtures(); setup(); await selectAgent(); await userEvent.click(screen.getByRole("tab", { name: "Chat" }));
    mock.mockImplementation(async (_url, options) => {
      const body = JSON.parse(String(options?.body)), task = { id: "task-one", contextId: "ctx-one", status: { state: "TASK_STATE_COMPLETED" }, artifacts: [{ parts: [{ text: body.method === "GetTask" ? "Refreshed output" : "Task answer" }] }] };
      return json({ jsonrpc: "2.0", id: body.id, result: body.method === "GetTask" ? task : { task } });
    });
    await userEvent.type(screen.getByLabelText("Agent prompt"), "First"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" })); await screen.findByText("Task answer");
    await userEvent.type(screen.getByLabelText("Agent prompt"), "Follow up"); await userEvent.click(screen.getByRole("button", { name: "Send to agent" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled());
    const sends = mock.mock.calls.filter(([url]) => String(url).startsWith("/a2a/"));
    expect(JSON.parse(String(sends[1][1]?.body)).params.message).toMatchObject({ taskId: "task-one", contextId: "ctx-one" });
    await userEvent.click(screen.getByRole("button", { name: "Refresh agent task" })); expect(await screen.findByText("Refreshed output")).toBeInTheDocument();
  });
});
