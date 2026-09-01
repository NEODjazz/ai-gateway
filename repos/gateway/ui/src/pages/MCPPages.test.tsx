import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { AuthProvider } from "../auth/AuthContext";
import { MCPServersPage, MCPToolsetsPage } from "./MCPPages";

const json = (payload: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } }));
const connector = "mcp:weather@https://mcp.example.test/v1";
const server = { id: "weather", label: "Weather production", description: "Forecast tools", server_url: "https://mcp.example.test/v1", transport: "streamable-http", tools: [connector], enabled: true };
const toolset = { id: "weather-read", name: "Weather read", description: "Read-only weather", tools: [connector], enabled: true };

function renderPage(page: "servers" | "toolsets") {
  sessionStorage.setItem("ai-gateway.admin-token", "token");
  return render(<MemoryRouter><AuthProvider>{page === "servers" ? <MCPServersPage /> : <MCPToolsetsPage />}</AuthProvider></MemoryRouter>);
}

describe("MCP management pages", () => {
  afterEach(() => { vi.restoreAllMocks(); sessionStorage.clear(); });

  it("creates server policy metadata with a canonical connector suggestion", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/mcp/servers?expand=references" && !options?.method) return json({ data: [server], references: { weather: { toolset_ids: ["weather-read"] } } });
      if (url === "/admin/v1/mcp/servers/finance" && options?.method === "PUT") return json({ ...server, id: "finance" });
      return json({ data: [] });
    });
    renderPage("servers");
    expect(await screen.findByText("Weather production")).toBeInTheDocument();
    expect(within(screen.getByText("Toolset links").closest("article")!).getByText("1")).toBeInTheDocument();
    expect(screen.getByText(/stores no MCP credentials or request headers/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for MCP server weather" }));
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeDisabled();

    await userEvent.click(screen.getByRole("button", { name: "Create MCP Server" }));
    const form = screen.getByRole("dialog", { name: "Create MCP server" });
    await userEvent.type(within(form).getByLabelText("MCP server ID"), "finance");
    await userEvent.type(within(form).getByLabelText("MCP server label"), "Finance production");
    await userEvent.type(within(form).getByLabelText("MCP server URL"), "https://finance.example.test/mcp/");
    expect(within(form).getByText("mcp:finance@https://finance.example.test/mcp")).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Use suggestion" }));
    await userEvent.click(within(form).getByRole("button", { name: "Create MCP server" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/mcp/servers/finance" && options?.method === "PUT")).toBe(true));
    const request = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/mcp/servers/finance" && options?.method === "PUT");
    expect(JSON.parse(String(request?.[1]?.body))).toMatchObject({ label: "Finance production", server_url: "https://finance.example.test/mcp/", tools: ["mcp:finance@https://finance.example.test/mcp"], enabled: true });
  });

  it("selects configured connectors and exposes fail-closed assignment impact", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, options) => {
      const url = String(input);
      if (url === "/admin/v1/session") return json({ roles: ["admin"], capabilities: ["admin"] });
      if (url === "/admin/v1/mcp/servers" && !options?.method) return json({ data: [server] });
      if (url === "/admin/v1/mcp/toolsets?expand=references" && !options?.method) return json({ data: [toolset], references: { "weather-read": { access_group_ids: ["operators"], virtual_key_ids: ["vk-weather"] } } });
      if (url === "/admin/v1/mcp/toolsets/operations" && options?.method === "PUT") return json({ ...toolset, id: "operations" });
      return json({ data: [] });
    });
    renderPage("toolsets");
    expect(await screen.findByText("Weather read")).toBeInTheDocument();
    expect(within(screen.getByText("Assignments").closest("article")!).getByText("2")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Actions for MCP toolset weather-read" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "View assignments" }));
    expect(screen.getByText(/operators/)).toBeInTheDocument();
    expect(screen.getByText(/vk-weather/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Create MCP Toolset" }));
    const form = screen.getByRole("dialog", { name: "Create MCP toolset" });
    await userEvent.type(within(form).getByLabelText("MCP toolset ID"), "operations");
    await userEvent.type(within(form).getByLabelText("MCP toolset name"), "Operations");
    await userEvent.click(within(form).getByLabelText("Connector grants"));
    await userEvent.click(within(form).getByRole("option", { name: /Weather production/ }));
    await userEvent.click(within(form).getByRole("button", { name: "Create MCP toolset" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, options]) => url === "/admin/v1/mcp/toolsets/operations" && options?.method === "PUT")).toBe(true));
    const create = fetchMock.mock.calls.find(([url, options]) => url === "/admin/v1/mcp/toolsets/operations" && options?.method === "PUT");
    expect(JSON.parse(String(create?.[1]?.body))).toMatchObject({ name: "Operations", tools: [connector], enabled: true });

    await userEvent.click(screen.getByRole("button", { name: "Actions for MCP toolset weather-read" }));
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeDisabled();
  });
});
