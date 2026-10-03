import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { APIClient } from "../../api/client";
import { ResourceControls } from "./ResourceControls";
import { emptyResources, withResources } from "./resources";
import { playgroundConnection } from "./requests";

const connection = playgroundConnection(new APIClient(() => "console-key"), "custom", "test-key", "https://gateway.example.test/v1");
const catalog = { mcp_servers: [{ id: "weather", name: "Weather" }], mcp_toolsets: [{ id: "read", name: "Read only", server_ids: ["weather"], tool_grants: { weather: ["forecast"] } }], policies: ["strict"], tags: ["work"], agents: [], truncated: false };
const json = (value: unknown) => new Response(JSON.stringify(value));
function Harness({ model = "model" }: { model?: string }) {
  const [value, onUpdate] = useState(emptyResources);
  return <><ResourceControls connection={connection} model={model} endpoint="responses" disabled={false} value={value} onUpdate={onUpdate} /><pre aria-label="Configured request">{JSON.stringify(withResources({}, "responses", value))}</pre></>;
}
async function open() { await userEvent.click(screen.getByText("Tools, resources and policies")); }
describe("Playground resource controls", () => {
  it("discovers with the selected credential, filters toolsets and preserves selected schemas", async () => {
    const schema = { type: "object", properties: { count: { type: "integer", minimum: 1 } } };
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => String(path).includes("catalog") ? json(catalog) : json({ tools: [{ name: "forecast", inputSchema: schema }, { name: "delete", inputSchema: {} }] }));
    render(<Harness />); await open(); expect(mock).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Load resource catalog" }));
    await screen.findByLabelText("MCP toolset");
    expect(mock.mock.calls[0][0]).toBe("https://gateway.example.test/v1/playground/catalog?model=model");
    expect(new Headers(mock.mock.calls[0][1]?.headers).get("Authorization")).toBe("Bearer test-key");
    expect(mock.mock.calls[0][1]?.credentials).toBe("omit");
    await userEvent.click(screen.getByLabelText("MCP toolset")); await userEvent.click(screen.getByRole("option", { name: "Read only" }));
    await userEvent.click(screen.getByLabelText("Tool discovery server")); await userEvent.click(screen.getByRole("option", { name: "Weather" }));
    await userEvent.click(screen.getByRole("button", { name: "Load MCP tools" }));
    await userEvent.click(await screen.findByLabelText("MCP function forecast"));
    expect(screen.queryByLabelText("MCP function delete")).not.toBeInTheDocument();
    expect(JSON.parse(screen.getByLabelText("Configured request").textContent || "{}").tools[0].parameters).toEqual(schema);
    await userEvent.click(screen.getByLabelText("Prompt policy strict")); await userEvent.click(screen.getByLabelText("Request tag work"));
    expect(screen.getByLabelText("Configured request")).toHaveTextContent('playground_tags');
    await userEvent.click(screen.getByLabelText("MCP toolset")); await userEvent.click(screen.getByRole("option", { name: "All accessible servers" }));
    expect(screen.getByLabelText("Configured request")).not.toHaveTextContent('"tools"');
  });
  it("paginates owned vector stores and selects code files only for automatic containers", async () => {
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(async (path) => {
      if (String(path).includes("vector_stores")) return String(path).includes("after=") ? json({ data: [{ id: "vs_two", name: "Two" }], has_more: false, last_id: "vs_two" }) : json({ data: [{ id: "vs_one", name: "One" }], has_more: true, last_id: "vs_one" });
      return json({ data: [{ id: "file_one", filename: "brief.pdf" }], has_more: false, last_id: "file_one" });
    });
    render(<Harness />); await open(); await userEvent.click(screen.getByRole("button", { name: "Load vector stores" }));
    await userEvent.click(await screen.findByLabelText("Vector store One"));
    await userEvent.click(screen.getByRole("button", { name: "Load more vector stores" }));
    await userEvent.click(await screen.findByLabelText("Vector store Two"));
    expect(mock.mock.calls[1][0]).toContain("after=vs_one");
    await userEvent.click(screen.getByLabelText("Enable code interpreter")); await userEvent.click(screen.getByRole("button", { name: "Load files" }));
    await userEvent.click(await screen.findByLabelText("Code file brief.pdf"));
    const request = JSON.parse(screen.getByLabelText("Configured request").textContent || "{}");
    expect(request.tools).toEqual([{ type: "file_search", vector_store_ids: ["vs_one", "vs_two"] }, { type: "code_interpreter", container: { type: "auto", file_ids: ["file_one"] } }]);
  });
  it("discards old discovery results when the model scope changes", async () => {
    let resolve!: (response: Response) => void;
    const mock = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise<Response>((done) => resolve = done));
    const view = render(<Harness />); await open(); await userEvent.click(screen.getByRole("button", { name: "Load resource catalog" }));
    await waitFor(() => expect(mock).toHaveBeenCalledOnce());
    view.rerender(<Harness model="other-model" />);
    await act(async () => resolve(json(catalog)));
    expect(mock.mock.calls[0][1]?.signal?.aborted).toBe(true);
    expect(screen.queryByLabelText("Prompt policy strict")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Load resource catalog" })).toBeEnabled();
  });
  it("shows malformed catalog errors without hiding the conversation workspace", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(json({ ...catalog, mcp_servers: [{}] }));
    render(<Harness />); await open(); await userEvent.click(screen.getByRole("button", { name: "Load resource catalog" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("invalid resource catalog");
    expect(screen.getByLabelText("Configured request")).toBeInTheDocument();
  });
});
